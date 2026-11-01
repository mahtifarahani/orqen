package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/backup"
	"orqen/internal/policy"
)

// tildeHome replaces the home directory in a path with ~, so a support bundle carries no user name.
func tildeHome(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h) {
		return "~" + strings.TrimPrefix(p, h)
	}
	return p
}

func (a *App) mongoVersion(ctx context.Context) string {
	var out struct {
		Version string `bson:"version"`
	}
	if err := a.DB.Database.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&out); err != nil {
		return ""
	}
	return out.Version
}

// supportBundle gathers X36: versions, the latest preflight report and per-organization counts, extension states, the job
// failure codes of the last seven days, the last restore report and the backup status. It holds no documents, no texts and
// no connection URLs.
func (a *App) supportBundle(ctx context.Context) (bson.M, error) {
	now := time.Now().UTC()
	orgs, err := findMany[OrganizationDoc](ctx, a.DB, "organizations", bson.M{}, bson.D{{Key: "_id", Value: 1}}, 100)
	if err != nil {
		return nil, err
	}
	health := []bson.M{}
	preflight := []bson.M{}
	for _, o := range orgs {
		count := func(coll string, f bson.M) int64 {
			f["org_id"] = o.ID
			n, _ := a.DB.Coll(coll).CountDocuments(ctx, f)
			return n
		}
		health = append(health, bson.M{"org_id": o.ID, "slug": o.Slug, "connections": count("connections", bson.M{"active": true}), "projects": count("projects", bson.M{"active": true}),
			"runs_active": count("runs", bson.M{"active": true}), "jobs_active": count("jobs", bson.M{"active": true}), "extensions_enabled": count("extension_states", bson.M{"state": "enabled"})})
		st, e := a.systemState(ctx, o.ID)
		if e != nil {
			return nil, e
		}
		preflight = append(preflight, bson.M{"org_id": o.ID, "report": st.preflightDTO()})
	}
	exts, err := findMany[ExtensionStateDoc](ctx, a.DB, "extension_states", bson.M{}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	extRows := []bson.M{}
	for _, d := range exts {
		row := bson.M{"org_id": d.OrgID, "id": d.ExtensionID, "state": d.State, "version": nil, "path": tildeHome(d.Path)}
		if d.Detected != nil {
			row["version"] = d.Detected.Version
		}
		extRows = append(extRows, row)
	}
	cur, err := a.DB.Coll("jobs").Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"last_error.code": bson.M{"$exists": true, "$ne": nil}, "updated_at": bson.M{"$gte": now.Add(-7 * 24 * time.Hour)}}},
		bson.M{"$group": bson.M{"_id": bson.M{"kind": "$kind", "code": "$last_error.code"}, "count": bson.M{"$sum": 1}}},
	})
	if err != nil {
		return nil, err
	}
	var groups []struct {
		ID    struct{ Kind, Code string } `bson:"_id"`
		Count int64                       `bson:"count"`
	}
	if err = cur.All(ctx, &groups); err != nil {
		return nil, err
	}
	failures := []bson.M{}
	for _, g := range groups {
		failures = append(failures, bson.M{"kind": g.ID.Kind, "code": g.ID.Code, "count": g.Count})
	}
	sort.Slice(failures, func(i, j int) bool {
		if failures[i]["kind"] != failures[j]["kind"] {
			return failures[i]["kind"].(string) < failures[j]["kind"].(string)
		}
		return failures[i]["code"].(string) < failures[j]["code"].(string)
	})
	var restore any
	if rows, e := findMany[bson.M](ctx, a.DB, "restore_reports", bson.M{}, bson.D{{Key: "created_at", Value: -1}}, 1); e != nil {
		return nil, e
	} else if len(rows) > 0 {
		restore = restoreReportDTO(rows[0])
	}
	s, err := a.backupSettings(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := findMany[BackupRunDoc](ctx, a.DB, "backup_runs", bson.M{}, bson.D{{Key: "started_at", Value: -1}}, 5)
	if err != nil {
		return nil, err
	}
	recent := []bson.M{}
	for _, r := range runs {
		recent = append(recent, r.DTO())
	}
	return bson.M{
		"generated_at": timestamp(now),
		"versions":     bson.M{"orqen": Version, "schema": SchemaVersion, "policy_defaults": policy.ProductDefaultsVersion, "go": runtime.Version(), "mongo": a.mongoVersion(ctx)},
		"preflight":    preflight, "health": health, "extensions": extRows, "job_failures_7d": failures, "restore": restore,
		"backup": bson.M{"enabled": s.Enabled, "interval_hours": s.IntervalHours, "keep_last": s.KeepLast, "keep_days": s.KeepDays, "recent_runs": recent},
	}, nil
}

func (a *App) getSupportBundle(w http.ResponseWriter, r *http.Request) error {
	b, err := a.supportBundle(r.Context())
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	// The bundle holds no secrets by construction; the scanner is the second line of defence.
	if hits := backup.NewScanner(nil).Scan(raw); len(hits) > 0 {
		return problem("support_bundle_secret_detected")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="orqen-support-bundle-`+time.Now().UTC().Format("20060102T150405Z")+`.json"`)
	w.WriteHeader(200)
	_, _ = w.Write(append(raw, '\n'))
	return nil
}
