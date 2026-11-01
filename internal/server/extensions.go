package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/extensions"
)

// ExtDetected is what Detect recorded about the registered binary.
type ExtDetected struct {
	Version string    `bson:"version"`
	SHA256  string    `bson:"sha256"`
	At      time.Time `bson:"at"`
}

// ExtLastTest is the last Test result.
type ExtLastTest struct {
	OK     bool      `bson:"ok"`
	Code   string    `bson:"code"`
	Report bson.M    `bson:"report"`
	At     time.Time `bson:"at"`
}

// ExtCred points at a secret in the credential store. The value is never stored in MongoDB.
type ExtCred struct {
	Ref   string    `bson:"credential_ref"`
	SetAt time.Time `bson:"set_at"`
}

// ExtHealth is the last health check (stat and sha256 only; it never executes the extension).
type ExtHealth struct {
	Status string    `bson:"status"` // ok | changed | missing | failed | ""
	Code   string    `bson:"code"`
	At     time.Time `bson:"at"`
}

// ExtensionStateDoc is one extension in one organization (collection extension_states).
type ExtensionStateDoc struct {
	ID              string                 `bson:"_id"`
	OrgID           string                 `bson:"org_id"`
	ExtensionID     string                 `bson:"extension_id"`
	State           string                 `bson:"state"`
	Path            string                 `bson:"path"`
	RegisteredAt    *time.Time             `bson:"registered_at"`
	ActiveInstallID string                 `bson:"active_install_id"`
	Detected        *ExtDetected           `bson:"detected"`
	LastTest        *ExtLastTest           `bson:"last_test"`
	License         *extensions.Acceptance `bson:"license"`
	Config          map[string]any         `bson:"config"`
	Credentials     map[string]ExtCred     `bson:"credentials"`
	Health          ExtHealth              `bson:"health"`
	LastError       string                 `bson:"last_error"`
	DisabledReason  string                 `bson:"disabled_reason"`
	Revision        int64                  `bson:"revision"`
	UpdatedAt       time.Time              `bson:"updated_at"`
}

// ExtensionInstallDoc is an immutable record of a successful Detect + Test (collection extension_installs).
type ExtensionInstallDoc struct {
	ID               string    `bson:"_id"`
	OrgID            string    `bson:"org_id"`
	ExtensionID      string    `bson:"extension_id"`
	Number           int       `bson:"number"`
	Path             string    `bson:"path"`
	Version          string    `bson:"version"`
	SHA256           string    `bson:"sha256"`
	CapabilityReport bson.M    `bson:"capability_report"`
	TestedAt         time.Time `bson:"tested_at"`
	CreatedAt        time.Time `bson:"created_at"`
}

// ExtensionBindingDoc enables an extension for one project (collection extension_project_bindings).
type ExtensionBindingDoc struct {
	ID          string    `bson:"_id"`
	OrgID       string    `bson:"org_id"`
	ProjectID   string    `bson:"project_id"`
	ExtensionID string    `bson:"extension_id"`
	Enabled     bool      `bson:"enabled"`
	Revision    int64     `bson:"revision"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

func (a *App) extensionRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}/extensions"
	return []Route{
		{"GET", p, a.listExtensions}, {"GET", p + "/{extId}", a.getExtension},
		{"PUT", p + "/{extId}/binary", a.registerExtensionBinary}, {"GET", p + "/{extId}/candidates", a.findExtensionCandidates},
		{"POST", p + "/{extId}/detect", a.extensionOperation("extension_detect", "detect")},
		{"PUT", p + "/{extId}/license", a.acceptExtensionLicense}, {"DELETE", p + "/{extId}/license", a.revokeExtensionLicense},
		{"PUT", p + "/{extId}/config", a.setExtensionConfig},
		{"PUT", p + "/{extId}/credentials/{field}", a.setExtensionCredential}, {"DELETE", p + "/{extId}/credentials/{field}", a.deleteExtensionCredential},
		{"POST", p + "/{extId}/test", a.extensionOperation("extension_test", "test")},
		{"POST", p + "/{extId}/enable", a.enableExtension}, {"POST", p + "/{extId}/disable", a.disableExtension},
		{"POST", p + "/{extId}/rollback", a.rollbackExtension},
		{"POST", p + "/{extId}/health", a.extensionOperation("extension_health", "health")},
		{"PUT", "/api/v1/orgs/{orgId}/projects/{prjId}/extensions/{extId}", a.bindProjectExtension},
	}
}

func init() {
	for _, id := range []string{"detect", "test", "health"} {
		idemRequired["POST /api/v1/orgs/{orgId}/extensions/{extId}/"+id] = true
	}
}

func manifestOf(r *http.Request) (*extensions.Manifest, error) {
	m, ok := extensions.Get(r.PathValue("extId"))
	if !ok {
		return nil, problem("not_found")
	}
	return m, nil
}

// extState returns the stored state, or an empty "absent" document at revision 0.
func (a *App) extState(ctx context.Context, org, ext string) (ExtensionStateDoc, error) {
	d, err := findOne[ExtensionStateDoc](ctx, a.DB, "extension_states", bson.M{"org_id": org, "extension_id": ext})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ExtensionStateDoc{OrgID: org, ExtensionID: ext, State: string(extensions.StatusAbsent)}, nil
	}
	return d, err
}

func extSnapshot(d ExtensionStateDoc) extensions.Snapshot {
	s := extensions.Snapshot{Status: extensions.Status(d.State), Path: d.Path, License: d.License, Config: d.Config, Credentials: map[string]bool{}}
	for f, c := range d.Credentials {
		s.Credentials[f] = c.Ref != ""
	}
	if d.Detected != nil {
		s.Version, s.SHA256 = d.Detected.Version, d.Detected.SHA256
	}
	return s
}

// usable is the single place the server asks whether an extension may run; ok means a Permit exists.
func (a *App) extUsable(ctx context.Context, org string, m *extensions.Manifest, project string) (extensions.Permit, string, error) {
	d, err := a.extState(ctx, org, m.ID)
	if err != nil {
		return extensions.Permit{}, "", err
	}
	s := extSnapshot(d)
	if project != "" {
		s.ProjectScoped = true
		b, e := findOne[ExtensionBindingDoc](ctx, a.DB, "extension_project_bindings", bson.M{"org_id": org, "project_id": project, "extension_id": m.ID})
		if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
			return extensions.Permit{}, "", e
		}
		s.ProjectBound = e == nil && b.Enabled
	}
	p, why := extensions.Usable(m, s)
	return p, why, nil
}

func (a *App) extConflict(ctx context.Context, org string, m *extensions.Manifest) error {
	c := problem("revision_conflict")
	if dto, err := a.extDetail(ctx, org, m); err == nil {
		c.Current = dto
	}
	return c
}

// extUpdate applies f to the stored state under an If-Match revision (0 creates the document) and appends an event.
func (a *App) extUpdate(ctx context.Context, org string, m *extensions.Manifest, expected int64, event string, payload bson.M, f func(*ExtensionStateDoc) error) (ExtensionStateDoc, error) {
	var out ExtensionStateDoc
	err := a.DB.Txn(ctx, func(sc context.Context) error {
		cur, err := a.extState(sc, org, m.ID)
		if err != nil {
			return err
		}
		if cur.Revision != expected {
			return a.extConflict(sc, org, m)
		}
		next := cur
		if err = f(&next); err != nil {
			return err
		}
		if err = a.extWrite(sc, cur, &next); err != nil {
			if errors.Is(err, errExtRace) {
				return a.extConflict(sc, org, m)
			}
			return err
		}
		out = next
		if event == "" {
			return nil
		}
		return a.AppendEvent(sc, org, event, Subject{"extension", m.ID}, payload, &out.Revision, nil, false)
	})
	if err == nil {
		a.Committed()
	}
	return out, err
}

var errExtRace = errors.New("extension state changed")

func (a *App) extWrite(sc context.Context, cur ExtensionStateDoc, next *ExtensionStateDoc) error {
	next.UpdatedAt = time.Now().UTC()
	next.Revision = cur.Revision + 1
	if cur.Revision == 0 {
		next.ID, next.OrgID, next.ExtensionID = newID("exs"), cur.OrgID, cur.ExtensionID
		_, err := a.DB.Coll("extension_states").InsertOne(sc, next)
		if mongo.IsDuplicateKeyError(err) {
			return errExtRace
		}
		return err
	}
	res, err := a.DB.Coll("extension_states").ReplaceOne(sc, bson.M{"_id": cur.ID, "org_id": cur.OrgID, "revision": cur.Revision}, next)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errExtRace
	}
	return nil
}

// extApply is the job-side update: no If-Match, the caller already holds a lease. f may veto by returning errExtSkip.
var errExtSkip = errors.New("skip")

func (a *App) extApply(sc context.Context, org string, m *extensions.Manifest, event string, payload bson.M, op *string, f func(*ExtensionStateDoc) error) error {
	cur, err := a.extState(sc, org, m.ID)
	if err != nil {
		return err
	}
	next := cur
	if err = f(&next); err != nil {
		if errors.Is(err, errExtSkip) {
			return nil
		}
		return err
	}
	if err = a.extWrite(sc, cur, &next); err != nil {
		return err
	}
	return a.AppendEvent(sc, org, event, Subject{"extension", m.ID}, payload, &next.Revision, op, true)
}

// extNext applies the pure state machine and maps a refusal to the route's problem.
func extNext(m *extensions.Manifest, d *ExtensionStateDoc, act extensions.Action) error {
	st := extensions.Status(d.State)
	next, err := extensions.Next(st, act)
	if err != nil {
		switch {
		case st == extensions.StatusAbsent:
			return problem("extension_not_registered")
		case st == extensions.StatusLicenseRequired:
			return problem("license_acceptance_required")
		case act == extensions.ActEnable:
			return problem("extension_not_tested")
		case act == extensions.ActTest:
			return problem("extension_not_detected")
		}
		return problem("extension_state_invalid")
	}
	d.State = string(next)
	return nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ---- DTOs

func manifestDTO(m *extensions.Manifest) bson.M {
	creds := []bson.M{}
	for _, c := range m.CredentialFields {
		creds = append(creds, bson.M{"field": c.Field, "label_key": c.LabelKey, "required": c.Required})
	}
	return bson.M{"id": m.ID, "version": m.Version, "source": m.Source, "kind": m.Kind, "integration": m.Integration, "transport": m.Transport,
		"license": bson.M{"spdx": m.License.SPDX, "gate": m.License.Gate, "notice_key": m.License.NoticeKey},
		"runtime": bson.M{"kind": m.Runtime.Kind, "min_version": m.Runtime.MinVersion}, "config_schema": m.ConfigSchema, "credential_fields": creds,
		"capabilities": nonNilStr(m.Capabilities), "read_tools": nonNilStr(m.ReadTools), "write_tools": nonNilStr(m.WriteTools), "supported_hosts": nonNilStr(m.SupportedHosts),
		"requires": bson.M{"docker": m.Requires.Docker}}
}

func usableDTO(m *extensions.Manifest, d ExtensionStateDoc) bson.M {
	_, why := extensions.Usable(m, extSnapshot(d))
	return bson.M{"ok": why == "", "reason": optStr(why)}
}

func licenseDTO(l *extensions.Acceptance) any {
	if l == nil {
		return nil
	}
	return bson.M{"license_id": l.LicenseID, "notice_sha256": l.NoticeSHA256, "basis": l.Basis, "note": l.Note, "accepted_at": timestamp(l.AcceptedAt), "revoked_at": optionalTime(l.RevokedAt)}
}

func (a *App) extSummary(m *extensions.Manifest, d ExtensionStateDoc) bson.M {
	var health any
	if d.Health.Status != "" {
		health = bson.M{"status": d.Health.Status, "code": optStr(d.Health.Code), "at": timestamp(d.Health.At)}
	}
	return bson.M{"manifest": manifestDTO(m), "state": d.State, "usable": usableDTO(m, d), "health": health, "revision": d.Revision}
}

func (a *App) extDetail(ctx context.Context, org string, m *extensions.Manifest) (bson.M, error) {
	d, err := a.extState(ctx, org, m.ID)
	if err != nil {
		return nil, err
	}
	installs, err := findMany[ExtensionInstallDoc](ctx, a.DB, "extension_installs", bson.M{"org_id": org, "extension_id": m.ID}, bson.D{{Key: "number", Value: -1}}, 50)
	if err != nil {
		return nil, err
	}
	bindings, err := findMany[ExtensionBindingDoc](ctx, a.DB, "extension_project_bindings", bson.M{"org_id": org, "extension_id": m.ID}, bson.D{{Key: "project_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	out := a.extSummary(m, d)
	ins := []bson.M{}
	for _, i := range installs {
		ins = append(ins, bson.M{"id": i.ID, "number": i.Number, "path": i.Path, "version": i.Version, "sha256": i.SHA256, "capability_report": i.CapabilityReport, "tested_at": timestamp(i.TestedAt), "active": i.ID == d.ActiveInstallID})
	}
	creds := []bson.M{}
	for _, c := range m.CredentialFields {
		creds = append(creds, bson.M{"field": c.Field, "set": d.Credentials[c.Field].Ref != ""})
	}
	bs := []bson.M{}
	for _, b := range bindings {
		bs = append(bs, bson.M{"project_id": b.ProjectID, "enabled": b.Enabled, "revision": b.Revision})
	}
	var det, lt any
	if d.Detected != nil {
		det = bson.M{"version": d.Detected.Version, "sha256": d.Detected.SHA256, "at": timestamp(d.Detected.At)}
	}
	if d.LastTest != nil {
		lt = bson.M{"ok": d.LastTest.OK, "code": optStr(d.LastTest.Code), "report": d.LastTest.Report, "at": timestamp(d.LastTest.At)}
	}
	notices := []bson.M{}
	for _, n := range extensions.Notices(m.ID) {
		notices = append(notices, bson.M{"locale": n.Locale, "text": n.Text, "sha256": n.SHA256})
	}
	cfg := d.Config
	if cfg == nil {
		cfg = map[string]any{}
	}
	out["path"], out["detected"], out["last_test"] = optStr(d.Path), det, lt
	out["registered_at"], out["last_error"], out["disabled_reason"] = optionalTime(d.RegisteredAt), optStr(d.LastError), optStr(d.DisabledReason)
	out["license_state"], out["license_notices"], out["config"] = licenseDTO(d.License), notices, cfg
	out["installs"], out["credentials"], out["project_bindings"] = ins, creds, bs
	out["install_guidance"] = bson.M{"text_key": m.InstallGuide.TextKey, "commands": nonNilStr(m.InstallGuide.Commands)}
	return out, nil
}

func (a *App) respondExtension(w http.ResponseWriter, r *http.Request, m *extensions.Manifest, status int) error {
	dto, err := a.extDetail(r.Context(), r.PathValue("orgId"), m)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(dto["revision"].(int64)))
	writeJSON(w, status, dto)
	return nil
}

// ---- routes

func (a *App) listExtensions(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	out := []bson.M{}
	for _, m := range extensions.All() {
		d, err := a.extState(r.Context(), org, m.ID)
		if err != nil {
			return err
		}
		out = append(out, a.extSummary(m, d))
	}
	writeJSON(w, 200, bson.M{"items": out})
	return nil
}

func (a *App) getExtension(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

// deniedExtensionDir reports whether a binary may not live there: the Orqen data directory (it holds secrets-adjacent
// state and is written by Orqen itself) is refused.
func (a *App) deniedExtensionDir(path string) bool {
	data := filepath.Clean(a.Config.DataDir)
	return data != "." && (path == data || strings.HasPrefix(path, data+string(filepath.Separator)))
}

func (a *App) registerExtensionBinary(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		Path string `json:"path"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	bad := func(msg string) error {
		e := problem("extension_path_invalid")
		e.Errors = []FieldError{{"path", msg}}
		return e
	}
	if req.Path == "" || !filepath.IsAbs(req.Path) || filepath.Clean(req.Path) != req.Path || len(req.Path) > 1024 {
		return bad("must be a clean absolute path")
	}
	if a.deniedExtensionDir(req.Path) {
		return bad("must not be inside the Orqen data directory")
	}
	// stat only: registering never executes the file
	fi, err := os.Stat(req.Path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return bad("must be an existing executable file")
	}
	org := r.PathValue("orgId")
	_, err = a.extUpdate(r.Context(), org, m, expected, "extension.registered", bson.M{"extension_id": m.ID, "path": req.Path}, func(d *ExtensionStateDoc) error {
		now := time.Now().UTC()
		d.State, d.Path, d.RegisteredAt = string(extensions.StatusRegistered), req.Path, &now
		d.Detected, d.LastTest, d.ActiveInstallID, d.LastError, d.DisabledReason = nil, nil, "", "", ""
		d.Health = ExtHealth{}
		if m.Gated() && (d.License == nil || d.License.RevokedAt != nil) {
			d.State = string(extensions.StatusLicenseRequired)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

// childPathDirs is the fixed search path for candidates and the child PATH.
var childPathDirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"}

func (a *App) findExtensionCandidates(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	names := []string{m.ID}
	if i := strings.Index(m.ID, "-"); i > 0 {
		names = append(names, m.ID[:i])
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, dir := range childPathDirs {
		for _, n := range names {
			p := filepath.Join(dir, n)
			if fi, e := os.Stat(p); e == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 && !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	writeJSON(w, 200, bson.M{"paths": paths})
	return nil
}

// extensionOperation starts detect, test or health as an operation.
func (a *App) extensionOperation(kind, name string) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		m, err := manifestOf(r)
		if err != nil {
			return err
		}
		key, err := idempotencyKey(r, true)
		if err != nil {
			return err
		}
		org := r.PathValue("orgId")
		route := "POST /api/v1/orgs/{orgId}/extensions/{extId}/" + name
		return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "extId": m.ID}, nil), func(ctx context.Context) (Output, error) {
			d, err := a.extState(ctx, org, m.ID)
			if err != nil {
				return Output{}, err
			}
			switch name {
			case "detect":
				if m.Gated() && (d.License == nil || d.License.RevokedAt != nil) && d.State != string(extensions.StatusAbsent) {
					return Output{}, problem("license_acceptance_required")
				}
				if _, e := extensions.Next(extensions.Status(d.State), extensions.ActDetect); e != nil {
					return Output{}, extNext(m, &d, extensions.ActDetect)
				}
			case "test":
				if m.Gated() && (d.License == nil || d.License.RevokedAt != nil) {
					return Output{}, problem("license_acceptance_required")
				}
				if e := extNext(m, &ExtensionStateDoc{State: d.State}, extensions.ActTest); e != nil {
					return Output{}, e
				}
			case "health":
				if d.State == string(extensions.StatusAbsent) {
					return Output{}, problem("extension_not_registered")
				}
			}
			return a.startOperation(ctx, org, kind, Subject{"extension", m.ID}, kind+":"+org+":"+m.ID, 2, bson.M{"extension_id": m.ID})
		})
	}
}

func (a *App) acceptExtensionLicense(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		Basis         string `json:"basis"`
		Note          string `json:"note"`
		ConfirmPhrase string `json:"confirm_phrase"`
		NoticeSHA256  string `json:"notice_sha256"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	acc, lerr := extensions.Accept(m, extensions.AcceptInput{Basis: req.Basis, Note: req.Note, ConfirmPhrase: req.ConfirmPhrase, NoticeSHA256: req.NoticeSHA256}, extensions.NoticeDigests(m.ID), time.Now())
	if lerr != nil {
		var le *extensions.LicenseError
		if errors.As(lerr, &le) {
			return problem(le.Code)
		}
		return lerr
	}
	_, err = a.extUpdate(r.Context(), r.PathValue("orgId"), m, expected, "extension.license_accepted", bson.M{"extension_id": m.ID, "license_id": acc.LicenseID, "basis": acc.Basis}, func(d *ExtensionStateDoc) error {
		d.License = &acc
		if d.State == string(extensions.StatusLicenseRequired) {
			d.State = string(extensions.StatusRegistered)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

func (a *App) revokeExtensionLicense(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	_, err = a.extUpdate(r.Context(), r.PathValue("orgId"), m, expected, "extension.license_revoked", bson.M{"extension_id": m.ID}, func(d *ExtensionStateDoc) error {
		if !m.Gated() {
			return problem("license_not_required")
		}
		if d.License == nil || d.License.RevokedAt != nil {
			return problem("extension_state_invalid")
		}
		now := time.Now().UTC()
		lic := *d.License
		lic.RevokedAt = &now
		d.License = &lic
		if d.State != string(extensions.StatusAbsent) {
			if err := extNext(m, d, extensions.ActRevokeLicense); err != nil {
				return err
			}
			d.DisabledReason = "license_revoked"
		}
		return nil
	})
	if err != nil {
		return err
	}
	if m.ID == "gitnexus" {
		// every code index of the organization stops being usable; the directories are deleted by the person
		if _, e := a.DB.Coll("code_indexes").UpdateMany(r.Context(), bson.M{"org_id": r.PathValue("orgId"), "state": bson.M{"$in": []string{"queued", "indexing", "ready"}}}, bson.M{"$set": bson.M{"state": "license_revoked"}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
	}
	return a.respondExtension(w, r, m, 200)
}

func (a *App) setExtensionConfig(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		Config map[string]any `json:"config"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.Config == nil {
		req.Config = map[string]any{}
	}
	if m.ConfigSchema == nil && len(req.Config) > 0 {
		return configInvalid("config", "unknown_property")
	}
	if m.ConfigSchema != nil {
		if issues := extensions.ValidateConfig(m.ConfigSchema, req.Config); len(issues) > 0 {
			return configInvalid(issues[0].Path, issues[0].Code)
		}
	}
	_, err = a.extUpdate(r.Context(), r.PathValue("orgId"), m, expected, "extension.config_changed", bson.M{"extension_id": m.ID}, func(d *ExtensionStateDoc) error {
		d.Config = req.Config
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

func configInvalid(path, code string) *AppError {
	e := problem("extension_config_invalid")
	e.Errors = []FieldError{{path, code}}
	return e
}

func (a *App) enableExtension(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	_, err = a.extUpdate(r.Context(), r.PathValue("orgId"), m, expected, "extension.enabled", bson.M{"extension_id": m.ID}, func(d *ExtensionStateDoc) error {
		if m.Integration == extensions.DetectOnly {
			return problem("extension_detect_only")
		}
		if m.Gated() && (d.License == nil || d.License.RevokedAt != nil) {
			return problem("license_acceptance_required")
		}
		if err := extNext(m, d, extensions.ActEnable); err != nil {
			return err
		}
		for _, c := range m.CredentialFields {
			if c.Required && d.Credentials[c.Field].Ref == "" {
				return problem("credential_missing")
			}
		}
		if m.ConfigSchema != nil {
			if issues := extensions.ValidateConfig(m.ConfigSchema, d.Config); len(issues) > 0 {
				return configInvalid(issues[0].Path, issues[0].Code)
			}
		}
		d.DisabledReason = ""
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

func (a *App) disableExtension(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	_, err = a.extUpdate(r.Context(), r.PathValue("orgId"), m, expected, "extension.disabled", bson.M{"extension_id": m.ID}, func(d *ExtensionStateDoc) error {
		if err := extNext(m, d, extensions.ActDisable); err != nil {
			return err
		}
		d.DisabledReason = "user"
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

func (a *App) rollbackExtension(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req struct {
		InstallID string `json:"install_id"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	if !validID("exi", req.InstallID) {
		return validation("install_id", "is not an install id")
	}
	inst, err := findOne[ExtensionInstallDoc](r.Context(), a.DB, "extension_installs", bson.M{"_id": req.InstallID, "org_id": org, "extension_id": m.ID})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return validation("install_id", "is not an install of this extension")
	}
	if err != nil {
		return err
	}
	sum, herr := sha256File(inst.Path)
	if herr != nil || sum != inst.SHA256 {
		return problem("extension_binary_changed")
	}
	_, err = a.extUpdate(r.Context(), org, m, expected, "extension.rolled_back", bson.M{"extension_id": m.ID, "install_number": inst.Number}, func(d *ExtensionStateDoc) error {
		if d.State == string(extensions.StatusAbsent) {
			return problem("extension_not_registered")
		}
		if m.Gated() && (d.License == nil || d.License.RevokedAt != nil) {
			return problem("license_acceptance_required")
		}
		d.Path, d.ActiveInstallID = inst.Path, inst.ID
		d.Detected = &ExtDetected{Version: inst.Version, SHA256: inst.SHA256, At: inst.TestedAt}
		d.LastTest = &ExtLastTest{OK: true, Report: inst.CapabilityReport, At: inst.TestedAt}
		if d.State == string(extensions.StatusChanged) || d.State == string(extensions.StatusDetected) || d.State == string(extensions.StatusTestFailed) || d.State == string(extensions.StatusDetectFailed) || d.State == string(extensions.StatusRegistered) {
			d.State = string(extensions.StatusTested)
		}
		d.Health = ExtHealth{Status: "ok", At: time.Now().UTC()}
		return nil
	})
	if err != nil {
		return err
	}
	return a.respondExtension(w, r, m, 200)
}

func (a *App) bindProjectExtension(w http.ResponseWriter, r *http.Request) error {
	m, err := manifestOf(r)
	if err != nil {
		return err
	}
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, prj := r.PathValue("orgId"), r.PathValue("prjId")
	if _, err = a.project(r.Context(), org, prj); err != nil {
		return err
	}
	var out ExtensionBindingDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		if req.Enabled {
			d, e := a.extState(sc, org, m.ID)
			if e != nil {
				return e
			}
			if d.State != string(extensions.StatusEnabled) {
				return problem("extension_not_enabled")
			}
		}
		cur, e := findOne[ExtensionBindingDoc](sc, a.DB, "extension_project_bindings", bson.M{"org_id": org, "project_id": prj, "extension_id": m.ID})
		if errors.Is(e, mongo.ErrNoDocuments) {
			cur, e = ExtensionBindingDoc{}, nil
		}
		if e != nil {
			return e
		}
		if cur.Revision != expected {
			return problem("revision_conflict")
		}
		next := cur
		next.Enabled, next.UpdatedAt, next.Revision = req.Enabled, time.Now().UTC(), expected+1
		if expected == 0 {
			next.ID, next.OrgID, next.ProjectID, next.ExtensionID = newID("epb"), org, prj, m.ID
			if _, e = a.DB.Coll("extension_project_bindings").InsertOne(sc, next); mongo.IsDuplicateKeyError(e) {
				return problem("revision_conflict")
			} else if e != nil {
				return e
			}
		} else {
			res, e := a.DB.Coll("extension_project_bindings").ReplaceOne(sc, bson.M{"_id": cur.ID, "revision": cur.Revision}, next)
			if e != nil {
				return e
			}
			if res.MatchedCount == 0 {
				return problem("revision_conflict")
			}
		}
		out = next
		return a.AppendEvent(sc, org, "extension.project_bound", Subject{"extension", m.ID}, bson.M{"extension_id": m.ID, "project_id": prj, "enabled": req.Enabled}, &out.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, bson.M{"project_id": prj, "extension_id": m.ID, "enabled": out.Enabled, "revision": out.Revision})
	return nil
}
