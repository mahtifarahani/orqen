package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/extwrite"
	"os"
	"time"
	"unicode/utf8"
)

// LivePhrase must be typed to enable live writes.
const LivePhrase = "ENABLE LIVE WRITES"

// XwsAllow is one allowlisted (connection, project).
type XwsAllow struct {
	ConnectionID      string    `bson:"connection_id" json:"connection_id"`
	ProviderProjectID string    `bson:"provider_project_id" json:"provider_project_id"`
	HostSnapshot      string    `bson:"host_snapshot" json:"host_snapshot"`
	PathSnapshot      string    `bson:"path_snapshot" json:"path_snapshot"`
	AddedAt           time.Time `bson:"added_at" json:"added_at"`
}

// ExternalWriteSettingsDoc is the organization's write mode (collection external_write_settings). It is not a policy
// key: nothing overrides it.
type ExternalWriteSettingsDoc struct {
	ID            string     `bson:"_id"`
	OrgID         string     `bson:"org_id"`
	Mode          string     `bson:"mode"` // dry_run | live
	Allowlist     []XwsAllow `bson:"allowlist"`
	Paused        bool       `bson:"paused"`
	PausedReason  string     `bson:"paused_reason"`
	Revision      int64      `bson:"revision"`
	ModeChangedAt *time.Time `bson:"mode_changed_at"`
}

func xwsID(org string) string { return "xws_" + org[4:] }

func (a *App) guard() extwrite.Guard { return extwrite.CurrentGuard(os.Getenv) }

// DTO is ExternalWriteSettingsDto.
func (d ExternalWriteSettingsDoc) DTO(g extwrite.Guard) bson.M {
	list := []bson.M{}
	for _, e := range d.Allowlist {
		list = append(list, bson.M{"connection_id": e.ConnectionID, "provider_project_id": e.ProviderProjectID, "host_snapshot": e.HostSnapshot, "path_snapshot": e.PathSnapshot, "added_at": timestamp(e.AddedAt)})
	}
	mode := d.Mode
	if mode == "" {
		mode = extwrite.ModeDryRun
	}
	return bson.M{"mode": mode, "allowlist": list, "paused": d.Paused, "paused_reason": optStr(d.PausedReason), "revision": d.Revision, "mode_changed_at": optionalTime(d.ModeChangedAt),
		"env_guard": bson.M{"build_channel": g.BuildChannel, "orqen_env": g.OrqenEnv, "scope": string(g.Scope), "kill_switch": g.KillSwitch}}
}

func (a *App) externalWriteSettings(ctx context.Context, org string) (ExternalWriteSettingsDoc, error) {
	d, err := findOne[ExternalWriteSettingsDoc](ctx, a.DB, "external_write_settings", bson.M{"_id": xwsID(org), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ExternalWriteSettingsDoc{ID: xwsID(org), OrgID: org, Mode: extwrite.ModeDryRun, Allowlist: []XwsAllow{}}, nil
	}
	return d, err
}

func (a *App) getExternalWriteSettings(w http.ResponseWriter, r *http.Request) error {
	d, err := a.externalWriteSettings(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO(a.guard()))
	return nil
}

// AllowInput names an allowlist entry in a request.
type AllowInput struct {
	ConnectionID      string `json:"connection_id"`
	ProviderProjectID string `json:"provider_project_id"`
}

// PutExternalWritesRequest is the body of PUT /external-writes.
type PutExternalWritesRequest struct {
	Mode          string       `json:"mode"`
	Allowlist     []AllowInput `json:"allowlist"`
	ConfirmPhrase *string      `json:"confirm_phrase"`
}

// PutExternalWritesPauseRequest is the body of PUT /external-writes/pause.
type PutExternalWritesPauseRequest struct {
	Paused bool    `json:"paused"`
	Reason *string `json:"reason"`
}

var errXwsExists = errors.New("settings exist")

func (a *App) xwsConflict(ctx context.Context, org string) error {
	cur, err := a.externalWriteSettings(ctx, org)
	if err != nil {
		return err
	}
	c := problem("revision_conflict")
	c.Current = cur.DTO(a.guard())
	return c
}

// validateAllowlist resolves entries to active GitLab connections and primary repositories of Orqen projects.
func (a *App) validateAllowlist(ctx context.Context, org string, in []AllowInput) ([]XwsAllow, error) {
	out := []XwsAllow{}
	seen := map[string]bool{}
	for i, e := range in {
		field := "allowlist[" + string(rune('0'+i%10)) + "]"
		key := e.ConnectionID + "/" + e.ProviderProjectID
		if seen[key] {
			continue
		}
		seen[key] = true
		if !validID("con", e.ConnectionID) || e.ProviderProjectID == "" || len(e.ProviderProjectID) > 64 {
			return nil, allowlistInvalid(field, "is not a connection and a project id")
		}
		conn, err := a.connection(ctx, org, e.ConnectionID)
		if errors.Is(err, mongo.ErrNoDocuments) || err == nil && conn.Provider != "gitlab" {
			return nil, allowlistInvalid(field, "must be an active GitLab connection")
		}
		if err != nil {
			return nil, err
		}
		n, err := a.DB.Coll("projects").CountDocuments(ctx, bson.M{"org_id": org, "active": true, "primary_repo_key": repoKey(org, e.ConnectionID, e.ProviderProjectID)})
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, allowlistInvalid(field, "must be the primary repository of an Orqen project")
		}
		path := ""
		if row, e2 := findOne[CatalogProjectDoc](ctx, a.DB, "catalog_projects", bson.M{"org_id": org, "connection_id": e.ConnectionID, "provider_project_id": e.ProviderProjectID}); e2 == nil {
			path = row.PathWithNamespace
		}
		out = append(out, XwsAllow{ConnectionID: e.ConnectionID, ProviderProjectID: e.ProviderProjectID, HostSnapshot: hostOf(conn.BaseURL), PathSnapshot: path, AddedAt: time.Now().UTC()})
	}
	return out, nil
}

func allowlistInvalid(field, msg string) *AppError {
	e := problem("allowlist_invalid")
	e.Errors = []FieldError{{field, msg}}
	return e
}

func (a *App) putExternalWriteSettings(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutExternalWritesRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Mode != extwrite.ModeDryRun && req.Mode != extwrite.ModeLive {
		return validation("mode", "must be dry_run or live")
	}
	org := r.PathValue("orgId")
	var doc ExternalWriteSettingsDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		cur, err := a.externalWriteSettings(ctx, org)
		if err != nil {
			return err
		}
		if cur.Revision != expected {
			return a.xwsConflict(ctx, org)
		}
		list, err := a.validateAllowlist(ctx, org, req.Allowlist)
		if err != nil {
			return err
		}
		changedList := len(list) != len(cur.Allowlist)
		for i := range list {
			if i < len(cur.Allowlist) && (list[i].ConnectionID != cur.Allowlist[i].ConnectionID || list[i].ProviderProjectID != cur.Allowlist[i].ProviderProjectID) {
				changedList = true
			}
		}
		if req.Mode == extwrite.ModeLive {
			if len(list) == 0 {
				return allowlistInvalid("allowlist", "live mode needs at least one allowlisted project")
			}
			if (cur.Mode != extwrite.ModeLive || changedList) && (req.ConfirmPhrase == nil || *req.ConfirmPhrase != LivePhrase) {
				return problem("confirm_phrase_mismatch")
			}
		}
		now := time.Now().UTC()
		next := cur
		next.Mode, next.Allowlist = req.Mode, list
		if next.Mode != cur.Mode || cur.ModeChangedAt == nil {
			next.ModeChangedAt = &now
		}
		if expected == 0 {
			next.Revision = 1
			if _, e := a.DB.Coll("external_write_settings").InsertOne(ctx, next); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errXwsExists
				}
				return e
			}
		} else {
			e := a.DB.Coll("external_write_settings").FindOneAndUpdate(ctx, bson.M{"_id": xwsID(org), "org_id": org, "revision": expected}, bson.M{"$set": bson.M{"mode": next.Mode, "allowlist": next.Allowlist, "mode_changed_at": next.ModeChangedAt}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next)
			if errors.Is(e, mongo.ErrNoDocuments) {
				return a.xwsConflict(ctx, org)
			}
			if e != nil {
				return e
			}
		}
		doc = next
		if cur.Mode != next.Mode || (expected == 0 && next.Mode != cur.Mode) {
			if e := a.AppendEvent(ctx, org, "external_writes.mode_changed", Subject{"external_writes", xwsID(org)}, bson.M{"from": nonEmpty(cur.Mode, extwrite.ModeDryRun), "to": next.Mode}, &doc.Revision, nil, false); e != nil {
				return e
			}
		}
		if changedList {
			return a.AppendEvent(ctx, org, "external_writes.allowlist_changed", Subject{"external_writes", xwsID(org)}, bson.M{"count": len(list)}, &doc.Revision, nil, false)
		}
		return nil
	})
	if errors.Is(err, errXwsExists) {
		return a.xwsConflict(r.Context(), org)
	}
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO(a.guard()))
	return nil
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func (a *App) putExternalWritesPause(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PutExternalWritesPauseRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	reason := ""
	if req.Reason != nil {
		if utf8.RuneCountInString(*req.Reason) > 500 {
			return validation("reason", "must be at most 500 characters")
		}
		reason = *req.Reason
	}
	org := r.PathValue("orgId")
	var doc ExternalWriteSettingsDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		cur, err := a.externalWriteSettings(ctx, org)
		if err != nil {
			return err
		}
		if cur.Revision != expected {
			return a.xwsConflict(ctx, org)
		}
		next := cur
		next.Paused, next.PausedReason = req.Paused, reason
		if expected == 0 {
			next.Revision = 1
			if _, e := a.DB.Coll("external_write_settings").InsertOne(ctx, next); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errXwsExists
				}
				return e
			}
		} else if e := a.DB.Coll("external_write_settings").FindOneAndUpdate(ctx, bson.M{"_id": xwsID(org), "org_id": org, "revision": expected}, bson.M{"$set": bson.M{"paused": next.Paused, "paused_reason": reason}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&next); e != nil {
			if errors.Is(e, mongo.ErrNoDocuments) {
				return a.xwsConflict(ctx, org)
			}
			return e
		}
		doc = next
		kind := "external_writes.resumed"
		if req.Paused {
			kind = "external_writes.paused"
		}
		return a.AppendEvent(ctx, org, kind, Subject{"external_writes", xwsID(org)}, bson.M{"paused": req.Paused}, &doc.Revision, nil, false)
	})
	if errors.Is(err, errXwsExists) {
		return a.xwsConflict(r.Context(), org)
	}
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, doc.DTO(a.guard()))
	return nil
}
