package catherd

import "testing"

func rec(rung, thread string) RunRecord {
	r := RunRecord{Rung: rung}
	if thread != "" {
		r.Thread = &thread
	}
	return r
}

func TestActualIndependence(t *testing.T) {
	cat, err := ParseCatalog(fakeCatalogJSON)
	if err != nil {
		t.Fatal(err)
	}
	worker := rec("codex:gpt-6-sol#medium", "t-w")
	cases := []struct {
		name      string
		workers   []RunRecord
		reviewers []RunRecord
		status    string
		reason    string
		shared    int
	}{
		{"different families", []RunRecord{worker}, []RunRecord{rec("codex:gpt-6.1-sol#high", "t-r")}, "independent", "", 0},
		{"failover onto the reviewer family", []RunRecord{worker, rec("codex:gpt-6.1-sol#medium", "t-w2")}, []RunRecord{rec("codex:gpt-6.1-sol#high", "t-r")}, "same_model", "", 1},
		{"same family across backends", []RunRecord{worker}, []RunRecord{rec("opencode:opencode/gpt-6-sol#high", "t-r")}, "same_model", "", 1},
		{"unknown reviewer rung", []RunRecord{worker}, []RunRecord{rec("codex:mystery#high", "t-r")}, "unknown", "", 0},
		{"unknown worker rung", []RunRecord{rec("codex:mystery#low", "t-w")}, []RunRecord{rec("codex:gpt-6.1-sol#high", "t-r")}, "unknown", "", 0},
		{"same thread", []RunRecord{worker}, []RunRecord{rec("codex:gpt-6.1-sol#high", "t-w")}, "unknown", "same_thread", 0},
		{"no thread recorded is not a shared thread", []RunRecord{rec("codex:gpt-6-sol#medium", "")}, []RunRecord{rec("codex:gpt-6.1-sol#high", "")}, "independent", "", 0},
		{"no worker", nil, []RunRecord{rec("codex:gpt-6.1-sol#high", "t-r")}, "unknown", "no_worker", 0},
		{"no reviewer", []RunRecord{worker}, nil, "unknown", "no_reviewer", 0},
	}
	for _, c := range cases {
		got := ActualIndependence(c.workers, c.reviewers, &cat)
		if got.Status != c.status || got.Reason != c.reason || len(got.Shared) != c.shared {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := ActualIndependence([]RunRecord{worker}, []RunRecord{rec("codex:gpt-6.1-sol#high", "t")}, nil); got.Status != "unknown" {
		t.Fatalf("without a catalog nothing can be matched: %+v", got)
	}
}
