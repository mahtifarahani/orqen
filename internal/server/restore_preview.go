package server

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/backup"
	"orqen/internal/catherd"
)

// targetOf describes the install a restore would write into: documents per included collection, whether counters holds
// only the pristine events counter, and the number of blobs below the artifacts directory.
func targetOf(ctx context.Context, db *DB, artifactRoot string) (backup.Target, error) {
	t := backup.Target{Counts: map[string]int64{}}
	for _, c := range backup.IncludedCollections() {
		n, err := db.Coll(c).CountDocuments(ctx, bson.M{})
		if err != nil {
			return t, err
		}
		t.Counts[c] = n
	}
	t.EventsOnly = t.Counts["counters"] == 0
	if t.Counts["counters"] > 0 {
		cur, err := db.Coll("counters").Find(ctx, bson.M{})
		if err != nil {
			return t, err
		}
		var docs []bson.M
		if err = cur.All(ctx, &docs); err != nil {
			return t, err
		}
		t.EventsOnly = len(docs) == 1 && docs[0]["_id"] == "events" && asInt64(docs[0]["seq"]) == 0
	}
	_ = filepath.WalkDir(artifactRoot, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && filepath.Base(filepath.Dir(p)) != ".tmp" {
			t.Blobs++
		}
		return nil
	})
	return t, nil
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// backupLines reads one collection of a backup as BSON documents (canonical Extended JSON lines).
func backupLines(b *backup.Backup, coll string, fn func(bson.M)) error {
	for _, c := range b.Manifest.Collections {
		if c.Name != coll {
			continue
		}
		return b.ReadCollection(coll, func(line []byte) error {
			var d bson.M
			if err := bson.UnmarshalExtJSON(line, true, &d); err != nil {
				return &backup.IntegrityError{File: c.File}
			}
			fn(d)
			return nil
		})
	}
	return nil
}

// profileProbe reports whether a catherd profile is visible in an organization's catherd home. It never creates the
// home: an organization without one has no profiles.
func (a *App) profileProbe(ctx context.Context) func(org, name string) bool {
	cache := map[string]map[string]bool{}
	return func(org, name string) bool {
		rows, ok := cache[org]
		if !ok {
			rows = map[string]bool{}
			cache[org] = rows
			if a.catherdReady() == nil && validID("org", org) {
				env := catherd.NewOrgEnv(a.Config.DataDir, org)
				if _, err := os.Stat(env.Home()); err == nil {
					if list, err := a.Catherd.ListProfiles(ctx, env); err == nil {
						for _, r := range list {
							rows[r.Name] = true
						}
					}
				}
			}
		}
		return rows[name]
	}
}

// previewBackup runs the read-only dry-run of X35 against THIS install: integrity of every file, schema compatibility,
// whether the install is empty, which credentials this credential store lacks, the runs a restore would hold and the
// profiles that would need a republish. It writes nothing.
func (a *App) previewBackup(ctx context.Context, b *backup.Backup) (backup.PreviewDTO, error) {
	target, err := targetOf(ctx, a.DB, a.artifactRoot())
	if err != nil {
		return backup.PreviewDTO{}, err
	}
	return buildPreview(ctx, b, target, a.Creds, a.profileProbe(ctx))
}

func buildPreview(ctx context.Context, b *backup.Backup, target backup.Target, creds CredentialStore, present func(org, name string) bool) (backup.PreviewDTO, error) {
	in := backup.PreviewInput{Name: b.Name, Manifest: b.Manifest, Verify: b.VerifyAll(), BuildSchema: SchemaVersion, Target: target}
	if !backup.AllOK(in.Verify) {
		// Nothing can be trusted to be read; the preview reports the integrity failure alone.
		return backup.Preview(in), nil
	}
	for _, coll := range credentialDocs {
		err := backupLines(b, coll, func(d bson.M) {
			for _, ref := range credentialRefsOf(coll, d) {
				_, ok, _ := creds.Get(ctx, ref.Ref)
				ref.Present = ok
				in.Credentials = append(in.Credentials, ref)
			}
		})
		if err != nil {
			return backup.PreviewDTO{}, err
		}
	}
	seen := map[string]bool{}
	err := backupLines(b, "runs", func(d bson.M) {
		if act, _ := d["active"].(bool); act {
			in.NonTerminalRuns++
		}
	})
	if err != nil {
		return backup.PreviewDTO{}, err
	}
	err = backupLines(b, "model_profile_versions", func(d bson.M) {
		if st, _ := d["state"].(string); st != "published" {
			return
		}
		org, _ := d["org_id"].(string)
		name, _ := d["catherd_name"].(string)
		if name == "" || seen[org+"/"+name] {
			return
		}
		seen[org+"/"+name] = true
		in.Profiles = append(in.Profiles, backup.ProfileRef{OrgID: org, Name: name, Present: present != nil && present(org, name)})
	})
	if err != nil {
		return backup.PreviewDTO{}, err
	}
	sort.Slice(in.Profiles, func(i, j int) bool {
		if in.Profiles[i].OrgID != in.Profiles[j].OrgID {
			return in.Profiles[i].OrgID < in.Profiles[j].OrgID
		}
		return in.Profiles[i].Name < in.Profiles[j].Name
	})
	return backup.Preview(in), nil
}
