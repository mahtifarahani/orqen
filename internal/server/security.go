package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orqen/internal/extensions"
	"orqen/internal/mrready"
	"orqen/internal/secscan"
	"orqen/internal/workspace"
)

// ScanConfigRef records what a scan was started with. The model name and budget are never defaults.
type ScanConfigRef struct {
	Model           string  `bson:"model"`
	BudgetMode      string  `bson:"budget_mode"`
	BudgetUSD       float64 `bson:"budget_usd"`
	MaxTurns        int     `bson:"max_turns"`
	PolicyVersionID string  `bson:"policy_version_id"`
}

// SecurityScanDoc is one scan of a run's reviewed head (collection security_scans).
type SecurityScanDoc struct {
	ID                 string        `bson:"_id"`
	OrgID              string        `bson:"org_id"`
	ProjectID          string        `bson:"project_id"`
	RunID              string        `bson:"run_id"`
	HeadSHA            string        `bson:"head_sha"`
	TargetKind         string        `bson:"target_kind"`
	Trigger            string        `bson:"trigger"` // manual | auto
	ToolVersion        string        `bson:"tool_version"`
	Config             ScanConfigRef `bson:"config_ref"`
	State              string        `bson:"state"` // queued | running | finished | cancelled
	Completion         string        `bson:"completion"`
	Exit               int           `bson:"exit"`
	TreeModified       bool          `bson:"tree_modified"`
	TreeDiffArtifactID string        `bson:"tree_diff_artifact_id"`
	ReportArtifactID   string        `bson:"report_artifact_id"`
	StdoutArtifactID   string        `bson:"stdout_artifact_id"`
	Counts             bson.M        `bson:"counts"`
	StartedAt          *time.Time    `bson:"started_at"`
	FinishedAt         *time.Time    `bson:"finished_at"`
	CreatedAt          time.Time     `bson:"created_at"`
	Revision           int64         `bson:"revision"`
}

// triggerOf maps a missing trigger (documents from before Sprint 7) to manual.
func triggerOf(t string) string {
	if t == "" {
		return "manual"
	}
	return t
}

func (d SecurityScanDoc) DTO() bson.M {
	counts := d.Counts
	if counts == nil {
		counts = bson.M{"by_severity": bson.M{}, "blocking": 0}
	}
	return bson.M{"id": d.ID, "project_id": d.ProjectID, "run_id": d.RunID, "head_sha": d.HeadSHA, "target_kind": d.TargetKind, "trigger": triggerOf(d.Trigger), "tool_version": optStr(d.ToolVersion),
		"config": bson.M{"model": d.Config.Model, "budget_mode": d.Config.BudgetMode, "budget_usd": d.Config.BudgetUSD, "max_turns": d.Config.MaxTurns}, "state": d.State, "completion": optStr(d.Completion),
		"tree_modified": d.TreeModified, "tree_diff_artifact_id": optStr(d.TreeDiffArtifactID), "report_artifact_id": optStr(d.ReportArtifactID), "stdout_artifact_id": optStr(d.StdoutArtifactID),
		"counts": counts, "started_at": optionalTime(d.StartedAt), "finished_at": optionalTime(d.FinishedAt), "created_at": timestamp(d.CreatedAt), "revision": d.Revision}
}

// SecurityFindingDoc reuses the review-finding vocabulary (collection security_findings).
type SecurityFindingDoc struct {
	ID              string         `bson:"_id"`
	OrgID           string         `bson:"org_id"`
	ScanID          string         `bson:"scan_id"`
	RunID           string         `bson:"run_id"`
	HeadSHA         string         `bson:"head_sha"`
	FindingKey      string         `bson:"finding_key"`
	Severity        string         `bson:"severity"`
	SeverityRaw     string         `bson:"severity_raw"`
	Blocking        bool           `bson:"blocking"`
	File            string         `bson:"file"`
	Line            int            `bson:"line"`
	LocationValid   bool           `bson:"location_valid"`
	CWE             string         `bson:"cwe"`
	Title           string         `bson:"title"`
	Body            string         `bson:"body"`
	ProofArtifactID string         `bson:"proof_artifact_id"`
	Status          string         `bson:"status"`
	Waiver          *FindingWaiver `bson:"waiver"`
	CreatedAt       time.Time      `bson:"created_at"`
	Revision        int64          `bson:"revision"`
}

func (d SecurityFindingDoc) DTO() bson.M {
	var waiver any
	if d.Waiver != nil {
		waiver = bson.M{"text": d.Waiver.Text, "at": timestamp(d.Waiver.At)}
	}
	return bson.M{"id": d.ID, "source": "security", "scan_id": d.ScanID, "run_id": d.RunID, "head_sha": d.HeadSHA, "finding_key": d.FindingKey, "severity": d.Severity, "severity_raw": d.SeverityRaw,
		"blocking": d.Blocking, "file": d.File, "line": d.Line, "location_valid": d.LocationValid, "cwe": optStr(d.CWE), "title": d.Title, "body": d.Body, "proof_artifact_id": optStr(d.ProofArtifactID),
		"status": d.Status, "waiver": waiver, "created_at": timestamp(d.CreatedAt), "revision": d.Revision}
}

func (a *App) securityRoutes() []Route {
	const p = "/api/v1/orgs/{orgId}"
	return []Route{
		{"GET", p + "/security/status", a.getSecurityStatus}, {"GET", p + "/security/scans", a.listSecurityScans},
		{"POST", p + "/runs/{runId}/security-scans", a.startSecurityScan}, {"GET", p + "/security/scans/{scnId}", a.getSecurityScan},
		{"POST", p + "/security/scans/{scnId}/cancel", a.cancelSecurityScan}, {"GET", p + "/security/scans/{scnId}/findings", a.listScanFindings},
		{"GET", p + "/runs/{runId}/security-findings", a.listRunSecurityFindings}, {"POST", p + "/security-findings/{sfdId}/waive", a.waiveSecurityFinding},
		{"GET", p + "/security-findings/{sfdId}/issue-draft", a.getSecurityIssueDraft},
	}
}

func init() { idemRequired["POST /api/v1/orgs/{orgId}/runs/{runId}/security-scans"] = true }

func (a *App) scan(ctx context.Context, org, id string) (SecurityScanDoc, error) {
	return findOne[SecurityScanDoc](ctx, a.DB, "security_scans", bson.M{"_id": id, "org_id": org})
}

func (a *App) strixUsable(ctx context.Context, org string) (extensions.Permit, string, error) {
	m, _ := extensions.Get("strix")
	return a.extUsable(ctx, org, m, "")
}

func (a *App) getSecurityStatus(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	_, why, err := a.strixUsable(r.Context(), org)
	if err != nil {
		return err
	}
	eff, _, _, err := a.effectivePolicy(r.Context(), org, "")
	if err != nil {
		return err
	}
	pv := policyValues{}
	for _, e := range eff.Entries {
		pv[e.Key] = e.Value
	}
	running, open, err := a.securityCounts(r.Context(), org)
	if err != nil {
		return err
	}
	writeJSON(w, 200, bson.M{"usable": bson.M{"ok": why == "", "reason": optStr(why)},
		"policy": bson.M{"scan": pv.Str("automation.security_scan"), "gate": pv.Str("security.gate"), "blocking_severity": pv.Str("security.blocking_severity"), "model_set": pv.Str("security.model") != "", "budget_mode": pv.Str("security.budget_mode")},
		"counts": bson.M{"scans_running": running, "open_blocking": open}})
	return nil
}

func (a *App) listSecurityScans(w http.ResponseWriter, r *http.Request) error {
	f := bson.M{"org_id": r.PathValue("orgId")}
	for _, k := range []string{"project_id", "run_id"} {
		if v := r.URL.Query().Get(k); v != "" {
			f[k] = v
		}
	}
	list, err := findMany[SecurityScanDoc](r.Context(), a.DB, "security_scans", f, bson.D{{Key: "created_at", Value: -1}}, 100)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for _, d := range list {
		items = append(items, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": nil})
	return nil
}

func (a *App) getSecurityScan(w http.ResponseWriter, r *http.Request) error {
	d, err := a.scan(r.Context(), r.PathValue("orgId"), r.PathValue("scnId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, d.DTO())
	return nil
}

// startSecurityScan queues a scan of the run's reviewed head. Model and budget must have been chosen by the person.
func (a *App) startSecurityScan(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req struct {
		TargetKind string `json:"target_kind"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org, runID := r.PathValue("orgId"), r.PathValue("runId")
	route := "POST /api/v1/orgs/{orgId}/runs/{runId}/security-scans"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "runId": runID}, req), func(ctx context.Context) (Output, error) {
		if req.TargetKind != "local_clone" {
			return Output{}, problem("scan_target_unsupported")
		}
		run, err := a.run(ctx, org, runID)
		if err != nil {
			return Output{}, err
		}
		_, out, err := a.queueScan(ctx, org, run, "manual", req.TargetKind)
		return out, err
	})
}

// errScanAlreadyQueued means an active job for the same (run, head) exists; the auto sweep treats it as a skip.
var errScanAlreadyQueued = errors.New("scan already queued")

// scanPolicy reads the effective policy a scan starts with. The model and budget must have been chosen by the person.
func (a *App) scanPolicy(ctx context.Context, org, project string) (ScanConfigRef, error) {
	eff, _, _, err := a.effectivePolicy(ctx, org, project)
	if err != nil {
		return ScanConfigRef{}, err
	}
	pv := policyValues{}
	for _, e := range eff.Entries {
		pv[e.Key] = e.Value
	}
	if pv.Str("security.model") == "" {
		return ScanConfigRef{}, problem("security_model_unset")
	}
	mode, usd := pv.Str("security.budget_mode"), pv.Int("security.budget_usd")
	if mode != "capped" && mode != "unlimited" || mode == "capped" && usd < 1 {
		return ScanConfigRef{}, problem("security_budget_unset")
	}
	cfg := ScanConfigRef{Model: pv.Str("security.model"), BudgetMode: mode, MaxTurns: pv.Int("security.max_turns")}
	if mode == "capped" {
		cfg.BudgetUSD = float64(usd)
	}
	return cfg, nil
}

// queueScan validates and queues a scan of the run's reviewed head. It is the one place the manual route and the auto
// sweep share: reviewed head, strix usable, docker, model and budget, then the scan document and its job (dedupe
// secscan:<run>:<head>). A manual start also creates an operation; an auto start is a system job without one.
func (a *App) queueScan(ctx context.Context, org string, run RunDoc, trigger, targetKind string) (SecurityScanDoc, Output, error) {
	if targetKind != "local_clone" {
		return SecurityScanDoc{}, Output{}, problem("scan_target_unsupported")
	}
	head := run.Heads.Reviewed
	if head == "" {
		return SecurityScanDoc{}, Output{}, problem("scan_head_unreviewed")
	}
	m, _ := extensions.Get("strix")
	_, why, err := a.strixUsable(ctx, org)
	if err != nil {
		return SecurityScanDoc{}, Output{}, err
	}
	if why != "" {
		return SecurityScanDoc{}, Output{}, notUsable(why)
	}
	if m.Requires.Docker && !a.dockerUsable(ctx) {
		return SecurityScanDoc{}, Output{}, problem("docker_unavailable")
	}
	cfg, err := a.scanPolicy(ctx, org, run.ProjectID)
	if err != nil {
		return SecurityScanDoc{}, Output{}, err
	}
	cfg.PolicyVersionID = run.Pinned.PolicyVersionID
	dedupe := "secscan:" + run.ID + ":" + head
	if trigger == "auto" {
		if n, e := a.DB.Coll("jobs").CountDocuments(ctx, bson.M{"org_id": org, "dedupe_key": dedupe, "active": true}); e != nil {
			return SecurityScanDoc{}, Output{}, e
		} else if n > 0 {
			return SecurityScanDoc{}, Output{}, errScanAlreadyQueued
		}
	}
	d := SecurityScanDoc{ID: newID("scn"), OrgID: org, ProjectID: run.ProjectID, RunID: run.ID, HeadSHA: head, TargetKind: targetKind, Trigger: trigger, Config: cfg, State: "queued", CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err = a.DB.Coll("security_scans").InsertOne(ctx, d); err != nil {
		return SecurityScanDoc{}, Output{}, err
	}
	if trigger == "auto" {
		if err = a.ensureJobTx(ctx, org, "security_scan", dedupe, 1, bson.M{"scan_id": d.ID, "trigger": "auto"}); err != nil {
			return SecurityScanDoc{}, Output{}, err
		}
		return d, Output{}, a.AppendEvent(ctx, org, "security.scan_auto_queued", Subject{"security_scan", d.ID}, bson.M{"run_id": run.ID}, nil, nil, true)
	}
	out, err := a.startOperation(ctx, org, "security_scan", Subject{"security_scan", d.ID}, dedupe, 1, bson.M{"scan_id": d.ID})
	return d, out, err
}

func (a *App) cancelSecurityScan(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	var out SecurityScanDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		e := a.DB.Coll("security_scans").FindOneAndUpdate(sc, bson.M{"_id": r.PathValue("scnId"), "org_id": org, "revision": expected, "state": bson.M{"$in": []string{"queued", "running"}}},
			bson.M{"$set": bson.M{"state": "cancelled", "completion": "failed", "finished_at": time.Now().UTC()}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out)
		if errors.Is(e, mongo.ErrNoDocuments) {
			return problem("revision_conflict")
		}
		if e != nil {
			return e
		}
		return a.AppendEvent(sc, org, "security.scan_cancelled", Subject{"security_scan", out.ID}, bson.M{"run_id": out.RunID}, &out.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, out.DTO())
	return nil
}

func (a *App) findingsOf(ctx context.Context, f bson.M) (bson.M, error) {
	docs, err := findMany[SecurityFindingDoc](ctx, a.DB, "security_findings", f, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	items := []bson.M{}
	for _, d := range docs {
		items = append(items, d.DTO())
	}
	return bson.M{"items": items}, nil
}

func (a *App) listScanFindings(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	if _, err := a.scan(r.Context(), org, r.PathValue("scnId")); err != nil {
		return err
	}
	out, err := a.findingsOf(r.Context(), bson.M{"org_id": org, "scan_id": r.PathValue("scnId")})
	if err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

// listRunSecurityFindings shows the findings of the newest finished scan of the run.
func (a *App) listRunSecurityFindings(w http.ResponseWriter, r *http.Request) error {
	org, run := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, run); err != nil {
		return err
	}
	scans, err := findMany[SecurityScanDoc](r.Context(), a.DB, "security_scans", bson.M{"org_id": org, "run_id": run, "state": "finished"}, bson.D{{Key: "created_at", Value: -1}}, 1)
	if err != nil {
		return err
	}
	if len(scans) == 0 {
		writeJSON(w, 200, bson.M{"items": []bson.M{}})
		return nil
	}
	out, err := a.findingsOf(r.Context(), bson.M{"org_id": org, "scan_id": scans[0].ID})
	if err != nil {
		return err
	}
	writeJSON(w, 200, out)
	return nil
}

func (a *App) waiveSecurityFinding(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req struct {
		Text string `json:"text"`
	}
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || utf8.RuneCountInString(text) > 2000 {
		return validation("text", "must be 1 to 2000 characters")
	}
	org, id := r.PathValue("orgId"), r.PathValue("sfdId")
	var out SecurityFindingDoc
	err = a.DB.Txn(r.Context(), func(sc context.Context) error {
		cur, e := findOne[SecurityFindingDoc](sc, a.DB, "security_findings", bson.M{"_id": id, "org_id": org})
		if e != nil {
			return e
		}
		if cur.Revision != expected {
			c := problem("revision_conflict")
			c.Current = cur.DTO()
			return c
		}
		if cur.Status != FindingOpen || !cur.Blocking {
			return problem("finding_not_waivable")
		}
		if e = a.DB.Coll("security_findings").FindOneAndUpdate(sc, bson.M{"_id": id, "revision": expected, "status": FindingOpen},
			bson.M{"$set": bson.M{"status": FindingWaived, "waiver": FindingWaiver{Text: text, At: time.Now().UTC()}}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&out); e != nil {
			return e
		}
		if e = a.AppendEvent(sc, org, "security.finding_waived", Subject{"run", out.RunID}, bson.M{"run_id": out.RunID, "finding_id": id}, nil, nil, false); e != nil {
			return e
		}
		return a.enqueueMRRefresh(sc, org, out.RunID)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(out.Revision))
	writeJSON(w, 200, out.DTO())
	return nil
}

func (a *App) getSecurityIssueDraft(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	f, err := findOne[SecurityFindingDoc](r.Context(), a.DB, "security_findings", bson.M{"_id": r.PathValue("sfdId"), "org_id": org})
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	tree := filepath.Join(secscan.OrgDir(a.Config.DataDir, org), f.ScanID, "target")
	sf := secscan.Finding{Title: f.Title, Severity: f.SeverityRaw, CWE: f.CWE, File: f.File, Line: f.Line, Description: f.Body}
	writeJSON(w, 200, bson.M{"markdown": secscan.IssueDraft(sf, tree, home, nil)})
	return nil
}

// enqueueMRRefresh makes the readiness badge of the run's merge request update after a scan or a waiver.
func (a *App) enqueueMRRefresh(sc context.Context, org, run string) error {
	mr, err := findOne[MergeRequestDoc](sc, a.DB, "merge_requests", bson.M{"org_id": org, "run_id": run, "active": true})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	return a.ensureJobTx(sc, org, "mr_refresh", "mrrefresh-sec:"+mr.ID, 1, bson.M{"run_id": run})
}

// securityFacts is the input of the mrready security_scan check; nil unless the gate is on.
func (a *App) securityFacts(ctx context.Context, run RunDoc, pv policyValues) *mrready.SecurityFacts {
	gate := pv.Str("security.gate")
	if gate != mrready.GateRequired && gate != mrready.GateAdvisory {
		return nil
	}
	f := &mrready.SecurityFacts{Gate: gate}
	scans, err := findMany[SecurityScanDoc](ctx, a.DB, "security_scans", bson.M{"org_id": run.OrgID, "run_id": run.ID, "state": bson.M{"$ne": "cancelled"}}, bson.D{{Key: "created_at", Value: -1}}, 1)
	if err != nil || len(scans) == 0 {
		return f
	}
	s := scans[0]
	f.ScanHead, f.Completion, f.Running = s.HeadSHA, s.Completion, s.State == "queued" || s.State == "running"
	n, _ := a.DB.Coll("security_findings").CountDocuments(ctx, bson.M{"org_id": run.OrgID, "scan_id": s.ID, "status": FindingOpen, "blocking": true})
	f.BlockingOpen = int(n)
	return f
}

// ---- the scan job

func (a *App) secScanner(ctx context.Context, org string, p extensions.Permit) secscan.Scanner {
	if a.Scanner != nil {
		return a.Scanner
	}
	c := secscan.NewCLI(p, secscan.OrgDir(a.Config.DataDir, org))
	c.Mode = a.extSandboxMode(ctx, org)
	c.Timeout = a.scanTimeout(ctx, org)
	return c
}

func (a *App) runSecurityScan(ctx context.Context, job JobDoc, lease Lease) error {
	org := job.OrgID
	id, _ := job.Payload["scan_id"].(string)
	d, err := a.scan(ctx, org, id)
	if err != nil {
		return err
	}
	finish := func(ok bool, code string) error {
		return a.fenced(ctx, lease, func(sc context.Context) error {
			if ok {
				if e := a.transitionOperation(sc, job, "succeeded", bson.M{"scan_id": id}); e != nil {
					return e
				}
			} else if e := a.transitionOperation(sc, job, "failed", bson.M{"code": code}); e != nil {
				return e
			}
			return a.completeJob(sc, lease)
		})
	}
	if d.State != "queued" {
		return finish(d.State == "finished", "scan_not_queued")
	}
	p, why, err := a.strixUsable(ctx, org)
	if err != nil {
		return err
	}
	if why != "" {
		_ = a.markScanFailed(ctx, lease, id, "failed")
		return finish(false, why)
	}
	mirror, git, err := a.indexMirror(ctx, org, CodeIndexDoc{ProjectID: d.ProjectID})
	if err != nil {
		_ = a.markScanFailed(ctx, lease, id, "failed")
		return finish(false, "mirror_failed")
	}
	m, _ := extensions.Get("strix")
	st, _ := a.extState(ctx, org, "strix")
	secrets, _, err := a.extensionSecretEnv(ctx, st, m)
	if err != nil {
		return err
	}
	fence := func(f func(context.Context) error) error { return a.fenced(ctx, lease, f) }
	if err = a.scanCore(ctx, org, d, mirror, git, a.secScanner(ctx, org, p), p.Version(), secrets["LLM_API_KEY"], fence); err != nil {
		if errors.Is(err, errLeaseLost) {
			return err
		}
		_ = a.markScanFailed(ctx, lease, id, "failed")
		return finish(false, "scan_failed")
	}
	return finish(true, "")
}

func (a *App) markScanFailed(ctx context.Context, lease Lease, id, completion string) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		now := time.Now().UTC()
		_, err := a.DB.Coll("security_scans").UpdateOne(sc, bson.M{"_id": id}, bson.M{"$set": bson.M{"state": "finished", "completion": completion, "finished_at": now}, "$inc": bson.M{"revision": int64(1)}})
		return err
	})
}

func (a *App) storeScanText(ctx context.Context, org, kind string, b []byte, scan string) (string, error) {
	blob, err := storeBlob(a.artifactRoot(), org, strings.NewReader(string(b)), 8<<20)
	if err != nil {
		return "", err
	}
	art, err := a.recordArtifact(ctx, org, blob, "application/json", kind, Subject{"security_scan", scan})
	return art.ID, err
}

// scanCore archives the reviewed head into a disposable tree, runs the scanner, classifies the outcome and records the
// result. The tree is hashed before and after: a scan that changed it is flagged and the change never leaves the tree.
func (a *App) scanCore(ctx context.Context, org string, d SecurityScanDoc, mirror string, git *workspace.Git, sc secscan.Scanner, version, key string, fence func(func(context.Context) error) error) error {
	root := secscan.OrgDir(a.Config.DataDir, org)
	base := filepath.Join(root, d.ID)
	tree, reports := filepath.Join(base, "target"), filepath.Join(base, "report")
	if !within(root, base) || !shaPattern.MatchString(d.HeadSHA) {
		return errors.New("invalid scan path or head")
	}
	_ = os.RemoveAll(base)
	defer os.RemoveAll(tree)
	if err := os.MkdirAll(tree, 0o700); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := fence(func(c context.Context) error {
		_, e := a.DB.Coll("security_scans").UpdateOne(c, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "running", "started_at": now, "tool_version": version}, "$inc": bson.M{"revision": int64(1)}})
		return e
	}); err != nil {
		return err
	}
	res, err := git.Run(ctx, mirror, workspace.Creds{}, 5*time.Minute, "archive", "--format=tar", d.HeadSHA)
	if err != nil {
		return err
	}
	if err = extractTarString(res.Stdout, tree); err != nil {
		return err
	}
	before, err := secscan.Manifest(tree)
	if err != nil {
		return err
	}
	req := secscan.Request{Tree: tree, ReportDir: reports, MaxTurns: d.Config.MaxTurns, BudgetUSD: d.Config.BudgetUSD, Model: d.Config.Model, LLMKey: key}
	raw, scanErr := sc.Scan(ctx, req)
	if errors.Is(scanErr, context.Canceled) || ctx.Err() != nil {
		return ctx.Err()
	}
	after, _ := secscan.Manifest(tree)
	diff := secscan.Diff(before, after)
	known := []string{}
	if key != "" {
		known = append(known, key)
	}
	home, _ := os.UserHomeDir()
	var rep *secscan.Report
	var parseErr error
	if scanErr == nil && raw.Report != nil {
		r, e := secscan.Parse(raw.Report, known)
		if e != nil {
			parseErr = e
		} else {
			rep = &r
		}
	}
	exit, timedOut := raw.Exit, raw.TimedOut
	if scanErr != nil {
		exit = -1
	}
	completion := secscan.Classify(exit, timedOut, rep, parseErr, tree)
	scrub := func(s string) string {
		s = secscan.Redact(s, known)
		s = strings.ReplaceAll(s, tree, "<repo>")
		if home != "" && home != "/" {
			s = strings.ReplaceAll(s, home, "~")
		}
		return s
	}
	var repArt, outArt, diffArt string
	if rep != nil {
		b, _ := json.Marshal(rep)
		repArt, err = a.storeScanText(ctx, org, "security_report", []byte(scrub(string(b))), d.ID)
		if err != nil {
			return err
		}
	}
	if outArt, err = a.storeScanText(ctx, org, "security_stdout", []byte(scrub(raw.Stdout)), d.ID); err != nil {
		return err
	}
	if !diff.Empty() {
		b, _ := json.Marshal(diff)
		if diffArt, err = a.storeScanText(ctx, org, "scan_tree_diff", b, d.ID); err != nil {
			return err
		}
	}
	threshold := "high"
	if eff, _, _, e := a.effectivePolicy(ctx, org, d.ProjectID); e == nil {
		for _, en := range eff.Entries {
			if en.Key == "security.blocking_severity" {
				threshold, _ = en.Value.(string)
			}
		}
	}
	fin := time.Now().UTC()
	return fence(func(c context.Context) error {
		counts := bson.M{}
		blocking := 0
		var docs []SecurityFindingDoc
		if rep != nil {
			for _, f := range rep.Findings {
				fd := SecurityFindingDoc{ID: newID("sfd"), OrgID: org, ScanID: d.ID, RunID: d.RunID, HeadSHA: d.HeadSHA, FindingKey: secscan.FindingKey(f), Severity: secscan.Map(f.Severity), SeverityRaw: f.Severity,
					Blocking: secscan.Blocking(f.Severity, threshold), File: f.File, Line: f.Line, CWE: f.CWE, Title: f.Title, Body: f.Description, Status: FindingOpen, CreatedAt: fin, Revision: 1}
				if f.File != "" {
					if _, ok := after[f.File]; ok {
						fd.LocationValid = true
					}
				}
				if f.Proof != "" {
					if b, e := json.Marshal(map[string]string{"proof": f.Proof}); e == nil {
						fd.ProofArtifactID, _ = a.storeScanText(c, org, "security_proof", b, d.ID)
					}
				}
				// the same finding of an earlier scan of this run keeps its waiver
				if old, e := findOne[SecurityFindingDoc](c, a.DB, "security_findings", bson.M{"org_id": org, "run_id": d.RunID, "finding_key": fd.FindingKey, "status": FindingWaived}); e == nil {
					fd.Status, fd.Waiver = FindingWaived, old.Waiver
				}
				if fd.Status == FindingOpen && fd.Blocking {
					blocking++
				}
				n, _ := counts[fd.Severity].(int)
				counts[fd.Severity] = n + 1
				docs = append(docs, fd)
			}
			for _, fd := range docs {
				if _, e := a.DB.Coll("security_findings").InsertOne(c, fd); e != nil {
					return e
				}
			}
		}
		if completion == secscan.Complete {
			keys := []string{}
			for _, fd := range docs {
				keys = append(keys, fd.FindingKey)
			}
			if _, e := a.DB.Coll("security_findings").UpdateMany(c, bson.M{"org_id": org, "run_id": d.RunID, "scan_id": bson.M{"$ne": d.ID}, "status": bson.M{"$in": []string{FindingOpen, FindingWaived}}, "finding_key": bson.M{"$nin": keys}},
				bson.M{"$set": bson.M{"status": FindingSuperseded}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
			if _, e := a.DB.Coll("security_findings").UpdateMany(c, bson.M{"org_id": org, "run_id": d.RunID, "scan_id": bson.M{"$ne": d.ID}, "status": FindingOpen, "finding_key": bson.M{"$in": keys}},
				bson.M{"$set": bson.M{"status": FindingSuperseded}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
				return e
			}
		}
		if _, e := a.DB.Coll("security_scans").UpdateOne(c, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "finished", "completion": string(completion), "exit": exit, "tree_modified": !diff.Empty(), "tree_diff_artifact_id": diffArt,
			"report_artifact_id": repArt, "stdout_artifact_id": outArt, "counts": bson.M{"by_severity": counts, "blocking": blocking}, "finished_at": fin}, "$inc": bson.M{"revision": int64(1)}}); e != nil {
			return e
		}
		if e := a.AppendEvent(c, org, "security.scan_finished", Subject{"security_scan", d.ID}, bson.M{"run_id": d.RunID, "completion": string(completion), "tree_modified": !diff.Empty()}, nil, nil, true); e != nil {
			return e
		}
		return a.enqueueMRRefresh(c, org, d.RunID)
	})
}
