package server

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/extensions"
)

// runPostRestoreCheck is X34's per-organization check after a restore. It never sets a credential or starts anything:
//   - every credential reference is looked up in THIS install's credential store; a missing one is marked explicitly
//     (a connection gets credential_missing, a publisher credential or catherd secret returns to "none")
//   - every published model profile version is looked for in the organization's catherd home; a missing one is marked
//     republish_required (the existing "Republish for current host" action fixes it)
//   - every extension is re-checked by a health job, which re-stats and re-hashes the binary
//
// Automation stays paused and runs stay held until the user resumes them.
func (a *App) runPostRestoreCheck(ctx context.Context, job JobDoc, lease Lease) error {
	org := job.OrgID
	missing := []bson.M{}
	type ref struct{ coll, id, ref, kind string }
	var refs []ref
	for _, coll := range credentialDocs {
		rows, err := findMany[bson.M](ctx, a.DB, coll, bson.M{"org_id": org}, bson.D{{Key: "_id", Value: 1}}, 5000)
		if err != nil {
			return err
		}
		for _, d := range rows {
			for _, c := range credentialRefsOf(coll, d) {
				refs = append(refs, ref{coll, c.Owner, c.Ref, c.Kind})
			}
		}
	}
	for _, r := range refs {
		_, ok, err := a.Creds.Get(ctx, r.ref)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, bson.M{"kind": r.kind, "owner": r.id})
		}
	}
	var republish []bson.M
	var listed map[string]bool
	if a.catherdReady() == nil {
		if env, err := a.catherdEnv(org); err == nil {
			if rows, e := a.Catherd.ListProfiles(ctx, env); e == nil {
				listed = map[string]bool{}
				for _, r := range rows {
					listed[r.Name] = true
				}
			}
		}
	}
	versions, err := findMany[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"org_id": org, "state": "published"}, bson.D{{Key: "_id", Value: 1}}, 5000)
	if err != nil {
		return err
	}
	exts, err := findMany[ExtensionStateDoc](ctx, a.DB, "extension_states", bson.M{"org_id": org}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return err
	}
	err = a.fenced(ctx, lease, func(sc context.Context) error {
		for _, r := range refs {
			if _, ok, _ := a.Creds.Get(sc, r.ref); ok {
				continue
			}
			var e error
			switch r.coll {
			case "connections":
				_, e = a.DB.Coll("connections").UpdateOne(sc, bson.M{"_id": r.id, "org_id": org}, bson.M{"$set": bson.M{"credential_state": "none", "credential_missing": true}, "$inc": bson.M{"revision": int64(1)}})
			case "publisher_credentials":
				_, e = a.DB.Coll("publisher_credentials").UpdateOne(sc, bson.M{"_id": r.id, "org_id": org}, bson.M{"$set": bson.M{"state": "none", "credential_missing": true}, "$inc": bson.M{"revision": int64(1)}})
			case "catherd_secrets":
				_, e = a.DB.Coll("catherd_secrets").UpdateOne(sc, bson.M{"_id": r.id, "org_id": org}, bson.M{"$set": bson.M{"state": "none", "credential_missing": true}, "$inc": bson.M{"revision": int64(1)}})
			}
			if e != nil {
				return e
			}
		}
		for _, v := range versions {
			if v.CatherdName == "" || listed == nil || listed[v.CatherdName] {
				continue
			}
			if _, e := a.DB.Coll("model_profile_versions").UpdateOne(sc, bson.M{"_id": v.ID}, bson.M{"$set": bson.M{"republish_required": true}}); e != nil {
				return e
			}
			republish = append(republish, bson.M{"profile_id": v.ProfileID, "version_id": v.ID, "name": v.CatherdName})
		}
		for _, x := range exts {
			if _, ok := extensions.Get(x.ExtensionID); !ok || x.Path == "" {
				continue
			}
			if e := a.ensureJobTx(sc, org, "extension_health", "extension_health:"+org+":"+x.ExtensionID, 2, bson.M{"extension_id": x.ExtensionID}); e != nil {
				return e
			}
		}
		if republish == nil {
			republish = []bson.M{}
		}
		last, e := findMany[bson.M](sc, a.DB, "restore_reports", bson.M{}, bson.D{{Key: "created_at", Value: -1}}, 1)
		if e != nil {
			return e
		}
		if len(last) > 0 {
			if _, e = a.DB.Coll("restore_reports").UpdateOne(sc, bson.M{"_id": last[0]["_id"]}, bson.M{"$set": bson.M{"post_check." + org: bson.M{"at": time.Now().UTC(), "missing_credentials": missing, "republish_required": republish}}}); e != nil {
				return e
			}
		}
		if e = a.AppendEvent(sc, org, "system.post_restore_checked", Subject{"organization", org}, bson.M{"missing_credentials": len(missing), "republish_required": len(republish)}, nil, nil, true); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
	return err
}
