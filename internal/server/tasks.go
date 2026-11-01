package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/runstate"
	"regexp"
	"time"
)

// runsByID loads the latest runs of the given tracked issues.
func (a *App) latestRuns(ctx context.Context, org string, docs []TrackedIssueDoc) (map[string]RunDoc, error) {
	ids := []string{}
	for _, d := range docs {
		if d.LatestRunID != nil {
			ids = append(ids, *d.LatestRunID)
		}
	}
	out := map[string]RunDoc{}
	if len(ids) == 0 {
		return out, nil
	}
	runs, err := findMany[RunDoc](ctx, a.DB, "runs", bson.M{"org_id": org, "_id": bson.M{"$in": ids}}, bson.D{{Key: "_id", Value: 1}}, int64(len(ids)))
	if err != nil {
		return nil, err
	}
	for _, r := range runs {
		out[r.ID] = r
	}
	return out, nil
}

func (a *App) listTasks(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	limit, after, err := pageQueryKey(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	filter := bson.M{"org_id": org}
	if v := q.Get("project_id"); v != "" {
		if !validID("prj", v) {
			return validation("project_id", "is not a project id")
		}
		filter["project_id"] = v
	}
	if v := q.Get("state"); v != "" {
		switch {
		case v == "candidate":
			filter["candidate"] = true
		case contains(runstate.States, v):
			filter["run_state"] = v
		default:
			return validation("state", "must be a run state or candidate")
		}
	}
	if v := q.Get("q"); v != "" {
		if len(v) > 64 {
			return validation("q", "must be at most 64 characters")
		}
		filter["key"] = bson.Regex{Pattern: "^" + regexp.QuoteMeta(v), Options: "i"}
	}
	docs, next, err := findPageBy(r.Context(), a.DB, "tracked_issues", filter, "sort_key", func(d TrackedIssueDoc) string { return d.SortKey }, limit, after)
	if err != nil {
		return err
	}
	runs, err := a.latestRuns(r.Context(), org, docs)
	if err != nil {
		return err
	}
	sec, err := a.taskSecurity(r.Context(), org, runs)
	if err != nil {
		return err
	}
	var positions map[string]int
	items := []bson.M{}
	for _, d := range docs {
		var run *RunDoc
		var pos *int
		if d.LatestRunID != nil {
			if x, ok := runs[*d.LatestRunID]; ok {
				run = &x
				if x.State == runstate.Queued && x.Active {
					if positions == nil {
						if positions, err = a.queuePositions(r.Context(), org); err != nil {
							return err
						}
					}
					if p, ok := positions[x.ID]; ok {
						pos = &p
					}
				}
			}
		}
		item := d.SummaryDTO(run, pos)
		if run != nil {
			item["security"] = sec[run.ID]
		}
		items = append(items, item)
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}

func (a *App) tracked(ctx context.Context, org, id string) (TrackedIssueDoc, error) {
	return findOne[TrackedIssueDoc](ctx, a.DB, "tracked_issues", bson.M{"_id": id, "org_id": org})
}

// TaskDTO is TaskDto: the summary plus the latest snapshot and the issue's runs.
func (d TrackedIssueDoc) TaskDTO(snap IssueSnapshotDoc, hasSnap bool, run *RunDoc, pos *int, runs []bson.M) bson.M {
	m := d.SummaryDTO(run, pos)
	if hasSnap {
		m["snapshot"] = snap.DTO()
	} else {
		m["snapshot"] = nil
	}
	if runs == nil {
		runs = []bson.M{}
	}
	m["runs"] = runs
	return m
}

func (a *App) getTask(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	ti, err := a.tracked(r.Context(), org, r.PathValue("issId"))
	if err != nil {
		return err
	}
	var snap IssueSnapshotDoc
	hasSnap := false
	if ti.LatestSnapshotID != "" {
		snap, err = findOne[IssueSnapshotDoc](r.Context(), a.DB, "issue_snapshots", bson.M{"_id": ti.LatestSnapshotID, "org_id": org})
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return err
		}
		hasSnap = err == nil
	}
	runs, err := findMany[RunDoc](r.Context(), a.DB, "runs", bson.M{"org_id": org, "tracked_issue_id": ti.ID}, bson.D{{Key: "_id", Value: -1}}, 20)
	if err != nil {
		return err
	}
	positions, err := a.queuePositions(r.Context(), org)
	if err != nil {
		return err
	}
	var latest *RunDoc
	var latestPos *int
	summaries := []bson.M{}
	for i := range runs {
		var pos *int
		if p, ok := positions[runs[i].ID]; ok {
			pos = &p
		}
		summaries = append(summaries, runs[i].SummaryDTO(pos))
		if ti.LatestRunID != nil && runs[i].ID == *ti.LatestRunID {
			latest, latestPos = &runs[i], pos
		}
	}
	dto := ti.TaskDTO(snap, hasSnap, latest, latestPos, summaries)
	if latest != nil {
		sec, err := a.taskSecurity(r.Context(), org, map[string]RunDoc{latest.ID: *latest})
		if err != nil {
			return err
		}
		dto["security"] = sec[latest.ID]
	}
	writeJSON(w, 200, dto)
	return nil
}

// securityCounts is the one place the organization's security numbers come from (the status route and the task summary).
func (a *App) securityCounts(ctx context.Context, org string) (running, openBlocking int64, err error) {
	if running, err = a.DB.Coll("security_scans").CountDocuments(ctx, bson.M{"org_id": org, "state": bson.M{"$in": []string{"queued", "running"}}}); err != nil {
		return 0, 0, err
	}
	openBlocking, err = a.DB.Coll("security_findings").CountDocuments(ctx, bson.M{"org_id": org, "status": FindingOpen, "blocking": true})
	return running, openBlocking, err
}

// taskSecurity summarises the newest non-cancelled scan of each run: one aggregate over security_scans and one over
// security_findings for the whole page. A run without a scan has no entry (the DTO field is null).
func (a *App) taskSecurity(ctx context.Context, org string, runs map[string]RunDoc) (map[string]bson.M, error) {
	out := map[string]bson.M{}
	if len(runs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	cur, err := a.DB.Coll("security_scans").Aggregate(ctx, []bson.M{
		{"$match": bson.M{"org_id": org, "run_id": bson.M{"$in": ids}, "state": bson.M{"$ne": "cancelled"}}},
		{"$sort": bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}},
		{"$group": bson.M{"_id": "$run_id", "scan_id": bson.M{"$first": "$_id"}, "head_sha": bson.M{"$first": "$head_sha"}, "state": bson.M{"$first": "$state"}, "completion": bson.M{"$first": "$completion"}}},
	})
	if err != nil {
		return nil, err
	}
	var scans []struct {
		RunID      string `bson:"_id"`
		ScanID     string `bson:"scan_id"`
		HeadSHA    string `bson:"head_sha"`
		State      string `bson:"state"`
		Completion string `bson:"completion"`
	}
	if err = cur.All(ctx, &scans); err != nil {
		return nil, err
	}
	if len(scans) == 0 {
		return out, nil
	}
	scanIDs := make([]string, 0, len(scans))
	for _, sc := range scans {
		scanIDs = append(scanIDs, sc.ScanID)
	}
	cur, err = a.DB.Coll("security_findings").Aggregate(ctx, []bson.M{
		{"$match": bson.M{"org_id": org, "scan_id": bson.M{"$in": scanIDs}, "status": FindingOpen, "blocking": true}},
		{"$group": bson.M{"_id": "$scan_id", "n": bson.M{"$sum": 1}}},
	})
	if err != nil {
		return nil, err
	}
	var counts []struct {
		ScanID string `bson:"_id"`
		N      int64  `bson:"n"`
	}
	if err = cur.All(ctx, &counts); err != nil {
		return nil, err
	}
	blocking := map[string]int64{}
	for _, c := range counts {
		blocking[c.ScanID] = c.N
	}
	for _, sc := range scans {
		run := runs[sc.RunID]
		out[sc.RunID] = bson.M{"scan_id": sc.ScanID, "head_sha": sc.HeadSHA, "state": sc.State, "completion": optStr(sc.Completion), "open_blocking": blocking[sc.ScanID], "stale": sc.HeadSHA != run.Heads.Reviewed}
	}
	return out, nil
}

func (a *App) evaluateTask(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("issId")
	route := "POST /api/v1/orgs/{orgId}/tasks/{issId}/evaluate"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "issId": id}, nil), func(ctx context.Context) (Output, error) {
		if paused, e := a.orgPaused(ctx, org); e != nil {
			return Output{}, e
		} else if paused {
			return Output{}, problem("automation_paused")
		}
		ti, err := a.tracked(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if ti.Inaccessible || ti.LatestSnapshotID == "" {
			return Output{}, problem("issue_inaccessible")
		}
		proj, err := a.project(ctx, org, ti.ProjectID)
		if err != nil {
			return Output{}, err
		}
		if proj.Mapping.Jira == nil {
			return Output{}, problem("jira_mapping_missing")
		}
		snap, err := findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": ti.LatestSnapshotID, "org_id": org})
		if err != nil {
			return Output{}, err
		}
		pv, err := a.livePolicy(ctx, org, proj.ID)
		if err != nil {
			return Output{}, err
		}
		run, created, err := a.applyIntentRun(ctx, proj, ti, snap, pv, "manual_evaluate")
		if err != nil {
			return Output{}, err
		}
		if run == nil {
			return Output{}, problem("issue_inaccessible")
		}
		dto, err := a.runDTO(ctx, *run)
		if err != nil {
			return Output{}, err
		}
		headers := map[string]string{"etag": etag(run.Revision)}
		if !created {
			dto["deduplicated"] = true
			return Output{200, dto, headers}, nil
		}
		headers["location"] = fmt.Sprintf("/api/v1/orgs/%s/runs/%s", org, run.ID)
		return Output{201, dto, headers}, nil
	})
}

func (a *App) pollProjectNow(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/intake/poll"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": id}, nil), func(ctx context.Context) (Output, error) {
		proj, err := a.project(ctx, org, id)
		if err != nil {
			return Output{}, err
		}
		if proj.Mapping.Jira == nil {
			return Output{}, problem("jira_mapping_missing")
		}
		if paused, e := a.orgPaused(ctx, org); e != nil {
			return Output{}, e
		} else if paused {
			return Output{}, problem("automation_paused")
		}
		return a.startOperation(ctx, org, "intake_poll", Subject{"project", id}, "intake_poll:"+id, 20, bson.M{"project_id": id, "manual": true})
	})
}

func (a *App) getProjectIntake(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	proj, err := a.project(r.Context(), org, id)
	if err != nil {
		return err
	}
	st, err := a.intakeState(r.Context(), org, id)
	if err != nil {
		return err
	}
	paused, err := a.orgPaused(r.Context(), org)
	if err != nil {
		return err
	}
	pv, err := a.livePolicy(r.Context(), org, id)
	if err != nil {
		return err
	}
	writeJSON(w, 200, st.DTO(proj.AutomationEnabled && proj.Mapping.Jira != nil, paused, pv.Str("automation.intake")))
	return nil
}

// TaskSummaryCountsDto numbers (S32); decisions and workspaces fill in with their work packages.
func (a *App) getTaskSummary(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	ctx := r.Context()
	count := func(coll, field string, filter bson.M) (bson.M, error) {
		cur, err := a.DB.Coll(coll).Aggregate(ctx, []bson.M{{"$match": filter}, {"$group": bson.M{"_id": "$" + field, "n": bson.M{"$sum": 1}}}})
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID string `bson:"_id"`
			N  int64  `bson:"n"`
		}
		if err = cur.All(ctx, &rows); err != nil {
			return nil, err
		}
		out := bson.M{}
		for _, row := range rows {
			out[row.ID] = row.N
		}
		return out, nil
	}
	runs, err := count("runs", "state", bson.M{"org_id": org, "active": true})
	if err != nil {
		return err
	}
	inbox, err := count("inbox_items", "reason", bson.M{"org_id": org, "status": "open"})
	if err != nil {
		return err
	}
	var open int64
	for _, v := range inbox {
		open += v.(int64)
	}
	spaces, err := count("workspaces", "state", bson.M{"org_id": org})
	if err != nil {
		return err
	}
	candidates, err := a.DB.Coll("tracked_issues").CountDocuments(ctx, bson.M{"org_id": org, "candidate": true})
	if err != nil {
		return err
	}
	states, err := findMany[IntakeStateDoc](ctx, a.DB, "intake_states", bson.M{"org_id": org}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	intakes := []bson.M{}
	for _, s := range states {
		var lastErr, blocked any
		if s.LastError != nil {
			lastErr = bson.M{"code": s.LastError.Code, "detail": s.LastError.Detail, "at": timestamp(s.LastError.At)}
		}
		if s.Blocked != nil {
			blocked = bson.M{"code": s.Blocked.Code, "connection_id": s.Blocked.ConnectionID}
		}
		var success any
		if s.LastSuccessStartedAt != nil {
			success = timestamp(*s.LastSuccessStartedAt)
		}
		intakes = append(intakes, bson.M{"project_id": s.ProjectID, "last_success_at": success, "last_error": lastErr, "waiting_network": s.WaitingNetwork, "blocked": blocked})
	}
	dec, err := a.decisionMetrics(ctx, org)
	if err != nil {
		return err
	}
	mrReady, err := a.DB.Coll("merge_requests").CountDocuments(ctx, bson.M{"org_id": org, "active": true, "state": "opened", "readiness.ready": true})
	if err != nil {
		return err
	}
	mrDry, err := a.DB.Coll("merge_requests").CountDocuments(ctx, bson.M{"org_id": org, "active": true, "state": "opened", "mode": "dry_run"})
	if err != nil {
		return err
	}
	xops, err := count("external_operations", "state", bson.M{"org_id": org, "state": bson.M{"$in": []string{XopAwaitingApproval, XopUnknown}}})
	if err != nil {
		return err
	}
	num := func(m bson.M, k string) int64 {
		n, _ := m[k].(int64)
		return n
	}
	secRunning, secOpen, err := a.securityCounts(ctx, org)
	if err != nil {
		return err
	}
	var holder any
	var held RunDoc
	if e := a.DB.Coll("runs").FindOne(ctx, bson.M{"org_id": org, "slot": bson.M{"$ne": nil}}).Decode(&held); e == nil {
		holder = held.ID
	}
	writeJSON(w, 200, bson.M{
		"merge_requests":      bson.M{"ready": mrReady, "awaiting_ci": num(runs, "awaiting_ci"), "dry_run": mrDry},
		"external_operations": bson.M{"awaiting_approval": num(xops, XopAwaitingApproval), "outcome_unknown": num(xops, XopUnknown)},
		"coding_slot":         bson.M{"run_id": holder, "queued": num(runs, "queued")},
		"security":            bson.M{"open_blocking": secOpen, "scans_running": secRunning},
		"runs_by_state":       runs, "inbox_open": open, "inbox_open_by_reason": inbox, "candidates": candidates, "intake": intakes, "decisions_24h": dec, "workspaces_by_state": spaces, "evaluated_at": timestamp(time.Now().UTC())})
	return nil
}

// decisionMetrics summarizes the decisions of the last 24 hours.
func (a *App) decisionMetrics(ctx context.Context, org string) (bson.M, error) {
	since := time.Now().UTC().Add(-24 * time.Hour)
	out := bson.M{"total": int64(0), "jev": int64(0), "fixed_rules": int64(0), "unsure": int64(0), "p50_latency_ms": int64(0)}
	decs, err := findMany[bson.M](ctx, a.DB, "decisions", bson.M{"org_id": org, "created_at": bson.M{"$gte": since}}, bson.D{{Key: "created_at", Value: 1}}, 5000)
	if err != nil {
		return nil, err
	}
	var lat []int64
	for _, d := range decs {
		switch d["status"] {
		case "ok":
			out["jev"] = out["jev"].(int64) + 1
		case "unsure", "error":
			out["unsure"] = out["unsure"].(int64) + 1
		}
		if l, ok := d["latency_ms"].(int64); ok && l > 0 {
			lat = append(lat, l)
		}
	}
	fixed, err := a.DB.Coll("runs").CountDocuments(ctx, bson.M{"org_id": org, "triage.source": "fixed_rules", "triage.decided_at": bson.M{"$gte": since}})
	if err != nil {
		return nil, err
	}
	out["fixed_rules"] = fixed
	out["total"] = out["jev"].(int64) + out["unsure"].(int64) + fixed
	if len(lat) > 0 {
		sortInt64(lat)
		out["p50_latency_ms"] = lat[len(lat)/2]
	}
	return out, nil
}
func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
