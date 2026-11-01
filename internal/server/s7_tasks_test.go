package server

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func s7Tracked(t *testing.T, e *scanEnv, run RunDoc, key string) TrackedIssueDoc {
	t.Helper()
	now := time.Now().UTC()
	id := newID("iss")
	ti := TrackedIssueDoc{ID: id, OrgID: e.org, ProjectID: e.prj, ConnectionID: "con_00000000000000000000000000", IssueID: id, Key: key, InFilter: true, LatestRunID: &run.ID, RunState: run.State,
		LastActivityAt: now, SortKey: taskSortKey(now, id), CreatedAt: now, AmbiguousProject: []string{}}
	if _, err := e.db.Coll("tracked_issues").InsertOne(context.Background(), ti); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Coll("runs").UpdateOne(context.Background(), bson.M{"_id": run.ID}, bson.M{"$set": bson.M{"tracked_issue_id": id}}); err != nil {
		t.Fatal(err)
	}
	return ti
}

func (e *scanEnv) taskItem(key string) map[string]any {
	e.t.Helper()
	list := e.json("GET", "/api/v1/orgs/"+e.org+"/tasks", nil, nil, 200)["items"].([]any)
	for _, x := range list {
		if m := x.(map[string]any); m["issue_key"] == key {
			return m
		}
	}
	e.t.Fatalf("task %s not listed", key)
	return nil
}

func TestS7TaskSecuritySummary(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	// a run in a held state so that no background sweep advances it
	if _, err := e.db.Coll("runs").UpdateOne(ctx, bson.M{"_id": e.rn.ID}, bson.M{"$set": bson.M{"hold": bson.M{"code": "dry_run", "detail": "", "since": time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	s7Tracked(t, e, e.rn, "ORQ-1")
	other := s7InsertRun(t, e.db, RunDoc{OrgID: e.org, ProjectID: e.prj, State: "discovered", Hold: &RunHold{Code: "dry_run", Since: time.Now().UTC()}})
	s7Tracked(t, e, other, "ORQ-2")
	if it := e.taskItem("ORQ-2"); it["security"] != nil {
		t.Fatal("a task without scans has security null:", it["security"])
	}

	d := e.scanDoc()
	if _, err := e.db.Coll("security_scans").UpdateOne(ctx, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "running"}}); err != nil {
		t.Fatal(err)
	}
	fd := SecurityFindingDoc{ID: newID("sfd"), OrgID: e.org, ScanID: d.ID, RunID: e.rn.ID, HeadSHA: e.sha, FindingKey: "k1", Severity: "major", SeverityRaw: "high", Blocking: true, Title: "t", Status: FindingOpen, CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err := e.db.Coll("security_findings").InsertOne(ctx, fd); err != nil {
		t.Fatal(err)
	}
	sum := e.json("GET", "/api/v1/orgs/"+e.org+"/tasks/summary", nil, nil, 200)["security"].(map[string]any)
	if sum["open_blocking"].(float64) != 1 || sum["scans_running"].(float64) != 1 {
		t.Fatal(sum)
	}
	st := e.json("GET", "/api/v1/orgs/"+e.org+"/security/status", nil, nil, 200)["counts"].(map[string]any)
	if st["open_blocking"].(float64) != 1 || st["scans_running"].(float64) != 1 {
		t.Fatal("the status route shares the helper:", st)
	}
	sec := e.taskItem("ORQ-1")["security"].(map[string]any)
	if sec["scan_id"] != d.ID || sec["open_blocking"].(float64) != 1 || sec["stale"] != false || sec["head_sha"] != e.sha {
		t.Fatal(sec)
	}
	if one := e.json("GET", "/api/v1/orgs/"+e.org+"/tasks/"+e.taskItem("ORQ-1")["id"].(string), nil, nil, 200); one["security"] == nil {
		t.Fatal("the task detail carries the summary too")
	}

	// waive: the blocking count drops to zero
	e.json("POST", "/api/v1/orgs/"+e.org+"/security-findings/"+fd.ID+"/waive", map[string]string{"text": "accepted"}, map[string]string{"If-Match": "1"}, 200)
	if sec = e.taskItem("ORQ-1")["security"].(map[string]any); sec["open_blocking"].(float64) != 0 {
		t.Fatal(sec)
	}
	if _, err := e.db.Coll("security_scans").UpdateOne(ctx, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"state": "finished", "completion": "complete"}}); err != nil {
		t.Fatal(err)
	}
	if sec = e.taskItem("ORQ-1")["security"].(map[string]any); sec["completion"] != "complete" || sec["state"] != "finished" {
		t.Fatal(sec)
	}
	// a new reviewed head makes the scan stale
	if _, err := e.db.Coll("runs").UpdateOne(ctx, bson.M{"_id": e.rn.ID}, bson.M{"$set": bson.M{"heads.reviewed": "ffffffffffffffffffffffffffffffffffffffff"}}); err != nil {
		t.Fatal(err)
	}
	if sec = e.taskItem("ORQ-1")["security"].(map[string]any); sec["stale"] != true {
		t.Fatal(sec)
	}
}

func TestS7TaskSecurityIsolatedPerOrg(t *testing.T) {
	e := newScanEnv(t)
	ctx := context.Background()
	foreign := "org_00000000000000000000000000"
	sc := SecurityScanDoc{ID: newID("scn"), OrgID: foreign, ProjectID: e.prj, RunID: e.rn.ID, HeadSHA: e.sha, TargetKind: "local_clone", State: "running", CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err := e.db.Coll("security_scans").InsertOne(ctx, sc); err != nil {
		t.Fatal(err)
	}
	fd := SecurityFindingDoc{ID: newID("sfd"), OrgID: foreign, ScanID: sc.ID, RunID: e.rn.ID, Blocking: true, Status: FindingOpen, CreatedAt: time.Now().UTC(), Revision: 1}
	if _, err := e.db.Coll("security_findings").InsertOne(ctx, fd); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Coll("runs").UpdateOne(ctx, bson.M{"_id": e.rn.ID}, bson.M{"$set": bson.M{"hold": bson.M{"code": "dry_run", "detail": "", "since": time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	s7Tracked(t, e, e.rn, "ORQ-1")
	sum := e.json("GET", "/api/v1/orgs/"+e.org+"/tasks/summary", nil, nil, 200)["security"].(map[string]any)
	if sum["open_blocking"].(float64) != 0 || sum["scans_running"].(float64) != 0 {
		t.Fatal("another organization's scans never count:", sum)
	}
	if e.taskItem("ORQ-1")["security"] != nil {
		t.Fatal("another organization's scan never shows on a task")
	}
}
