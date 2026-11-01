package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/catherd"
	"orqen/internal/sysprobe"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type PreflightCheck struct {
	ID         string   `bson:"id" json:"id"`
	Group      string   `bson:"group" json:"group"`
	Status     string   `bson:"status" json:"status"`
	Detail     string   `bson:"detail" json:"detail"`
	FixCode    string   `bson:"fix_code" json:"fix_code"`
	FixCommand []string `bson:"fix_command" json:"fix_command"`
	Required   bool     `bson:"required" json:"required"`
	Blocks     []string `bson:"blocks" json:"blocks"`
	DurationMS int64    `bson:"duration_ms" json:"duration_ms"`
}
type PreflightRecord struct {
	OperationID string           `bson:"operation_id" json:"operation_id"`
	CheckedAt   time.Time        `bson:"checked_at" json:"checked_at"`
	Checks      []PreflightCheck `bson:"checks" json:"checks"`
	ArtifactID  string           `bson:"artifact_id" json:"artifact_id"`
}
type SystemStateDoc struct {
	ID             string           `bson:"_id" json:"_id"`
	OrgID          string           `bson:"org_id" json:"org_id"`
	LastPreflight  *PreflightRecord `bson:"last_preflight" json:"last_preflight"`
	LaunchdOptedIn bool             `bson:"launchd_opted_in" json:"launchd_opted_in"`
	Revision       int64            `bson:"revision" json:"revision"`
	UpdatedAt      time.Time        `bson:"updated_at" json:"updated_at"`
}

func systemStateID(org string) string { return "sys_" + org[4:] }
func (a *App) systemState(ctx context.Context, org string) (SystemStateDoc, error) {
	d, err := findOne[SystemStateDoc](ctx, a.DB, "system_states", bson.M{"_id": systemStateID(org), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return SystemStateDoc{ID: systemStateID(org), OrgID: org}, nil
	}
	return d, err
}
func toPreflightChecks(in []sysprobe.Check) []PreflightCheck {
	out := make([]PreflightCheck, 0, len(in))
	for _, c := range in {
		out = append(out, PreflightCheck{ID: c.ID, Group: c.Group, Status: c.Status, Detail: c.Detail, FixCode: c.FixCode, FixCommand: c.FixCommand, Required: c.Required, Blocks: c.Blocks, DurationMS: c.DurationMS})
	}
	return out
}
func (d SystemStateDoc) preflightDTO() bson.M {
	out := bson.M{"checked_at": nil, "operation_id": nil, "artifact_id": nil, "checks": []PreflightCheck{}}
	if p := d.LastPreflight; p != nil {
		checks := p.Checks
		if checks == nil {
			checks = []PreflightCheck{}
		}
		out = bson.M{"checked_at": timestamp(p.CheckedAt), "operation_id": p.OperationID, "artifact_id": p.ArtifactID, "checks": checks}
	}
	return out
}

// startPreflight queues a new report; re-checking is always a new operation and never runs on a timer.
func (a *App) startPreflight(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/system/preflight"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, nil), func(ctx context.Context) (Output, error) {
		return a.startOperation(ctx, org, "system_preflight", Subject{"organization", org}, "system_preflight", 2, bson.M{})
	})
}

// getPreflight only reads the stored report.
func (a *App) getPreflight(w http.ResponseWriter, r *http.Request) error {
	st, err := a.systemState(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, st.preflightDTO())
	return nil
}

// probeKeychain stores, reads and deletes a throwaway credential. It may show an OS prompt, so it only runs on request.
func (a *App) probeKeychain(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/system/keychain-probe"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, nil), func(ctx context.Context) (Output, error) {
		ref := newID("cred")
		status, detail := "pass", "stored, read and removed a test credential"
		defer func() { _ = a.Creds.Delete(context.WithoutCancel(ctx), ref) }()
		if err := a.Creds.Put(ctx, ref, "orqen-preflight"); err != nil {
			status, detail = "fail", "the credential store refused a write"
		} else if v, ok, err := a.Creds.Get(ctx, ref); err != nil || !ok || v != "orqen-preflight" {
			status, detail = "fail", "the credential store did not return the test credential"
		}
		return Output{200, bson.M{"status": status, "detail": detail}, nil}, nil
	})
}
func (a *App) launchdPlan() sysprobe.LaunchdPlan {
	exe, _ := os.Executable()
	childPath := os.Getenv("PATH")
	if a.CatherdRT != nil {
		childPath = a.CatherdRT.ChildPath
	}
	abs, err := filepath.Abs(a.Config.DataDir)
	if err != nil {
		abs = a.Config.DataDir
	}
	return sysprobe.BuildLaunchdPlan(exe, abs, childPath)
}
func (a *App) getLaunchdPlan(w http.ResponseWriter, r *http.Request) error {
	st, err := a.systemState(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(st.Revision))
	writeJSON(w, 200, launchdDTO(st, a.launchdPlan()))
	return nil
}
func launchdDTO(st SystemStateDoc, p sysprobe.LaunchdPlan) bson.M {
	return bson.M{"opted_in": st.LaunchdOptedIn, "revision": st.Revision, "plist_path": p.PlistPath, "plist_xml": p.PlistXML, "install_commands": p.InstallCommands, "uninstall_commands": p.UninstallCommands}
}

type PatchLaunchdRequest struct {
	OptedIn *bool `json:"opted_in"`
}

// patchLaunchd records the opt-in only; nothing is written to ~/Library/LaunchAgents.
func (a *App) patchLaunchd(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PatchLaunchdRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.OptedIn == nil {
		return validation("opted_in", "is required")
	}
	org := r.PathValue("orgId")
	var doc SystemStateDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		now := time.Now().UTC()
		if expected == 0 {
			// Revision 0 is "no document yet" or a document only the preflight job has written.
			_, e := a.DB.Coll("system_states").UpdateOne(ctx, bson.M{"_id": systemStateID(org), "org_id": org, "revision": int64(0)}, bson.M{"$set": bson.M{"launchd_opted_in": *req.OptedIn, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, upsertOpts)
			if e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errDraftExists
				}
				return e
			}
			if doc, e = findOne[SystemStateDoc](ctx, a.DB, "system_states", bson.M{"_id": systemStateID(org)}); e != nil {
				return e
			}
		} else {
			e := a.DB.Coll("system_states").FindOneAndUpdate(ctx, bson.M{"_id": systemStateID(org), "org_id": org, "revision": expected}, bson.M{"$set": bson.M{"launchd_opted_in": *req.OptedIn, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&doc)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return errDraftExists
			}
			if e != nil {
				return e
			}
		}
		return a.AppendEvent(ctx, org, "system.launchd_updated", Subject{"system", doc.ID}, bson.M{"opted_in": doc.LaunchdOptedIn}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errDraftExists) {
		current, e := a.systemState(r.Context(), org)
		if e != nil {
			return e
		}
		conflict := problem("revision_conflict")
		conflict.Current = launchdDTO(current, a.launchdPlan())
		return conflict
	}
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, launchdDTO(doc, a.launchdPlan()))
	return nil
}

// preflightKeys summarizes catherd's keys from the key records and the published profile versions; no secret is read.
func (a *App) preflightKeys(ctx context.Context, org string) (sysprobe.KeyInputs, error) {
	k := sysprobe.KeyInputs{Backends: map[string]sysprobe.SecretState{}, OrgHome: catherd.NewOrgEnv(a.Config.DataDir, org).Home()}
	docs, err := a.secretDocs(ctx, org)
	if err != nil {
		return k, err
	}
	state := func(d CatherdSecretDoc) sysprobe.SecretState {
		return sysprobe.SecretState{Set: d.isSet(), Status: d.checkStatus()}
	}
	k.Jev = state(docs["jev"])
	for _, sk := range secretKinds {
		if sk.Backend != "" {
			k.Backends[sk.Backend] = state(docs[sk.Kind])
		}
	}
	versions, err := findMany[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"org_id": org, "state": "published"}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return k, err
	}
	iso := map[string]bool{}
	for _, v := range versions {
		doc := plain(v.Doc)
		if j, ok := doc["jev"].(map[string]any); ok && j["use"] == "auto" {
			k.JevInUse = true
		}
		for _, b := range isolatedKeyed(doc) {
			iso[b] = true
		}
	}
	for b := range iso {
		k.IsolatedBackends = append(k.IsolatedBackends, b)
	}
	sort.Strings(k.IsolatedBackends)
	return k, nil
}
func (a *App) preflightInputs(ctx context.Context, org string) (sysprobe.Inputs, error) {
	in := sysprobe.Inputs{CredentialStore: a.Config.CredentialStore, DataDir: a.Config.DataDir, Artifacts: filepath.Join(a.Config.DataDir, "artifacts"), CatherdHome: filepath.Join(a.Config.DataDir, "catherd"), Workspaces: filepath.Join(a.Config.DataDir, "workspaces"), UID: os.Getuid(), ChildPath: os.Getenv("PATH")}
	in.UserHome, _ = os.UserHomeDir()
	in.LaunchdPlist = filepath.Join(in.UserHome, "Library", "LaunchAgents", sysprobe.LaunchdLabel+".plist")
	name, err := a.DB.ReplicaSet(ctx)
	in.Mongo = sysprobe.MongoState{OK: err == nil, SetName: name}
	if err != nil {
		in.Mongo.Err = err.Error()
	} else if n, e := a.DB.Members(ctx); e == nil {
		in.Mongo.Members = n
	}
	if rt := a.CatherdRT; rt != nil {
		in.ChildPath, in.CatherdBin, in.CatherdVersion, in.CatherdSupported = rt.ChildPath, rt.Bin, rt.Version(), rt.Supported()
		if rt.Version() == "" {
			in.CatherdBin = ""
		}
	}
	conns, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 200)
	if err != nil {
		return in, err
	}
	for _, c := range conns {
		t := sysprobe.ConnTarget{ID: c.ID, Name: c.Name, BaseURL: c.BaseURL, Timeout: time.Duration(c.TimeoutMS) * time.Millisecond}
		if c.ProxyURL != nil {
			t.ProxyURL = *c.ProxyURL
		}
		if c.CustomCaPEM != nil {
			t.CustomCAPEM = *c.CustomCaPEM
		}
		in.Connections = append(in.Connections, t)
	}
	if in.Keys, err = a.preflightKeys(ctx, org); err != nil {
		return in, err
	}
	st, err := a.systemState(ctx, org)
	in.LaunchdOptIn = st.LaunchdOptedIn
	return in, err
}
