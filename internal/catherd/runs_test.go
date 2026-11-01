package catherd

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"testing"
)

//go:embed testdata/runs-show.json
var runsShowFixture []byte

func TestParseRunView(t *testing.T) {
	v, err := ParseRunView(runsShowFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Records) != 2 || len(v.Dispatches) != 3 || len(v.Landed) != 1 || v.Landed[0] != "M1" {
		t.Fatalf("%+v", v)
	}
	w := v.Records[0]
	if w.Status != "ok" || w.Rung != "codex:gpt-6-sol#medium" || w.Thread == nil || *w.Thread == "" || w.CostUSD != nil || w.ChangedOwned[0] != "src/feature.txt" || w.Lane == nil || *w.Lane != "M1.L1" {
		t.Fatalf("%+v", w)
	}
	if _, ok := w.Extra["futureField"]; !ok {
		t.Fatal("unknown fields are kept in Extra")
	}
	r := v.Records[1]
	if r.Status != "limit" || r.FailoverFrom == nil || *r.FailoverFrom != "codex:gpt-6-sol#high" || r.CostUSD == nil || r.Error == nil || r.Error.Code != "usage_limit" || r.Thread != nil {
		t.Fatalf("%+v", r)
	}
	// dispatches carry their record when finished and none while running
	if v.Dispatches[0].Record == nil || v.Dispatches[0].Record.DispatchID != "d-0001" || v.Dispatches[0].ExitReason != "exited" || v.Dispatches[2].Record != nil {
		t.Fatalf("%+v", v.Dispatches)
	}
	if got := v.Dispatches[2].AdmittedTime(); got.IsZero() || got.Hour() != 10 {
		t.Fatal(got)
	}
}

func TestParseRunRecordIsStrict(t *testing.T) {
	var base map[string]any
	var raw struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(runsShowFixture, &raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw.Records[0], &base)
	for _, key := range requiredRecordKeys {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		delete(m, key)
		b, _ := json.Marshal(m)
		if _, err := ParseRunRecord(b); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("a record without %s must be refused: %v", key, err)
		}
	}
	bad := map[string]any{}
	for k, v := range base {
		bad[k] = v
	}
	bad["status"] = "weird"
	b, _ := json.Marshal(bad)
	if _, err := ParseRunRecord(b); err == nil {
		t.Fatal("unknown status")
	}
	bad["status"], bad["schema"] = "ok", 2
	b, _ = json.Marshal(bad)
	if _, err := ParseRunRecord(b); err == nil {
		t.Fatal("unsupported schema")
	}
}

func TestFakeRunsWithoutBackend(t *testing.T) {
	f := NewFake()
	env := NewOrgEnv(t.TempDir(), "org_01HZZZZZZZZZZZZZZZZZZZZZZZ")
	if _, err := f.RunsShow(context.Background(), env, "r-1"); err == nil {
		t.Fatal("no backend must fail")
	}
	if err := f.ProfileUseRepo(context.Background(), env, HostCodex, "o-x-v1", "/repo"); err != nil || f.BoundProfile("/repo") != "o-x-v1" {
		t.Fatal(err, f.BoundProfile("/repo"))
	}
	if len(f.CallsOf("profile_use_repo")) != 1 {
		t.Fatal(f.Calls())
	}
}
