package learnbench

import (
	"encoding/json"
	"testing"
)

func TestSuiteAndDeterminism(t *testing.T) {
	s, err := Load(SuiteFixtureV1)
	if err != nil || len(s.Tasks) != 6 || s.Hash == "" {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err = Load("nope"); err == nil {
		t.Fatal("unknown suite")
	}
	// Every needed fact is taught by an earlier task (the suite is sequential).
	taught := map[string]bool{}
	for _, task := range s.Tasks {
		for _, n := range task.Needs {
			if !taught[n] {
				t.Errorf("task %s needs %s before any earlier task teaches it", task.ID, n)
			}
		}
		for _, f := range task.Teaches {
			taught[f] = true
		}
	}
	a, _ := json.Marshal(RunBoth(s, FakeEngine{}))
	b, _ := json.Marshal(RunBoth(s, FakeEngine{}))
	if string(a) != string(b) {
		t.Fatal("not deterministic")
	}
}

func TestOffArmReworksOncePerNeededFact(t *testing.T) {
	s, _ := Load(SuiteFixtureV1)
	off := Run(s, FakeEngine{}, ArmOff, nil)
	need := 0
	for i, task := range s.Tasks {
		need += len(task.Needs)
		if off.PerTask[i].Reworks != len(task.Needs) {
			t.Errorf("%s: %d reworks for %d needs", task.ID, off.PerTask[i].Reworks, len(task.Needs))
		}
	}
	if off.Metrics.SourcesNeeded != need || off.Metrics.ReworkRate != float64(need)/6 {
		t.Fatalf("%+v", off.Metrics)
	}
	// build_cmd is needed by t2, t3 and t6: it repeats twice; log_format (t3, t5) once.
	if off.Metrics.RepeatedIssueCount == 0 {
		t.Fatal("repeated issues expected in the off arm")
	}
}

func TestOnArmLearnsAndMetricsArithmetic(t *testing.T) {
	s, _ := Load(SuiteFixtureV1)
	r := RunBoth(s, FakeEngine{})
	on, off := r.Arms[ArmOn].Metrics, r.Arms[ArmOff].Metrics
	if on.ReworkRate != 0 || on.RepeatedIssueCount != 0 || on.SourcesNeeded != 0 || on.ReviewerFirstPassRate != 1 {
		t.Fatalf("on arm: %+v", on)
	}
	if off.ReviewerFirstPassRate != 1.0/6 { // only t1 needs nothing
		t.Fatalf("off first pass: %v", off.ReviewerFirstPassRate)
	}
	if off.EstTokens != off.SourcesNeeded*reworkTokens {
		t.Fatalf("off tokens: %d", off.EstTokens)
	}
	if on.EstTokens == 0 || on.EstTokens >= off.EstTokens {
		t.Fatalf("on tokens %d vs off %d", on.EstTokens, off.EstTokens)
	}
	if r.ClaimLevel != "harness_only" || r.Engine != "fake" || r.SuiteHash != s.Hash {
		t.Fatalf("%+v", r)
	}
	// A custom packet function that drops one fact shows exactly one rework where it is needed.
	drop := func(task Task, learned []string) Packet {
		var f []string
		for _, x := range learned {
			if x != "lint_cmd" {
				f = append(f, x)
			}
		}
		return Packet{Facts: f}
	}
	res := Run(s, FakeEngine{}, ArmOn, drop)
	if res.Metrics.SourcesNeeded != 1 || res.PerTask[3].Reworks != 1 || res.PerTask[3].Missing[0] != "lint_cmd" {
		t.Fatalf("%+v", res.PerTask)
	}
}
