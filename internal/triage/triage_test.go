package triage

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"orqen/internal/decision"
)

func okInput() GuardInput {
	return GuardInput{StatusCategory: "new", HasApprovedTest: true, AcceptanceText: "* The export button downloads a CSV.", AcceptanceSource: "section", TextForPaths: []string{"Add export to the table"}}
}
func codes(fs []Finding) string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.QuestionCode)
	}
	return strings.Join(out, ",")
}
func TestGuardsOrderedAndCollected(t *testing.T) {
	if fs := Guards(okInput()); len(fs) != 0 {
		t.Fatal(codes(fs))
	}
	cases := []struct {
		name string
		mut  func(*GuardInput)
		want string
	}{
		{"done", func(g *GuardInput) { g.StatusCategory = "done" }, "already_addressed"},
		{"resolved", func(g *GuardInput) { g.Resolution = "Fixed" }, "already_addressed"},
		{"inaccessible", func(g *GuardInput) { g.Inaccessible = true }, "issue_inaccessible"},
		{"two projects", func(g *GuardInput) { g.ProjectsClaim = 2 }, "issue_in_two_projects"},
		{"mapping", func(g *GuardInput) { g.ReadinessBlockers = []string{"primary_repo_missing", "profile_not_bound"} }, "mapping_incomplete"},
		{"jira mapping", func(g *GuardInput) { g.ReadinessBlockers = []string{"jira_mapping_missing"} }, "mapping_incomplete"},
		{"profile blocker is not mapping", func(g *GuardInput) { g.ReadinessBlockers = []string{"profile_not_bound", "catherd_doctor_missing"} }, ""},
		{"no test", func(g *GuardInput) { g.HasApprovedTest = false }, "no_test_command"},
		{"docker", func(g *GuardInput) { g.NeedsDocker, g.DockerFailed = true, true }, "docker_unavailable"},
		{"docker fine", func(g *GuardInput) { g.NeedsDocker = true }, ""},
		{"blocked", func(g *GuardInput) { g.Blockers = []Blocker{{"ORQ-5", "indeterminate"}, {"ORQ-6", "done"}} }, "blocked_by"},
		{"blocker done", func(g *GuardInput) { g.Blockers = []Blocker{{"ORQ-6", "done"}} }, ""},
		{"no acceptance", func(g *GuardInput) { g.AcceptanceSource, g.AcceptanceText = "none", "" }, "missing_acceptance"},
		{"short acceptance", func(g *GuardInput) { g.AcceptanceText = "* works" }, "missing_acceptance"},
		{"answer counts", func(g *GuardInput) {
			g.AcceptanceSource, g.AcceptanceText, g.AnswerAcceptance = "none", "", "The search box filters by date range."
		}, ""},
		{"short answer", func(g *GuardInput) { g.AcceptanceSource, g.AcceptanceText, g.AnswerAcceptance = "none", "", "ok" }, "missing_acceptance"},
		{"protected", func(g *GuardInput) {
			g.TextForPaths = []string{"Update .gitlab-ci.yml and src/auth/login.go"}
			g.Protected = []ProtectedPattern{{".gitlab-ci.yml", "deny"}, {"src/auth/**", "review"}}
		}, "protected_change"},
		{"review pattern does not stop", func(g *GuardInput) {
			g.TextForPaths = []string{"Touch src/auth/login.go"}
			g.Protected = []ProtectedPattern{{"src/auth/**", "review"}}
		}, ""},
	}
	for _, c := range cases {
		g := okInput()
		c.mut(&g)
		if got := codes(Guards(g)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	// All findings are collected; the first decides.
	g := okInput()
	g.StatusCategory, g.HasApprovedTest, g.AcceptanceSource, g.AcceptanceText = "done", false, "none", ""
	fs := Guards(g)
	if codes(fs) != "already_addressed,no_test_command,missing_acceptance" || fs[0].Outcome != OutcomeAlreadyAddressed || fs[0].Order != 1 || fs[2].Order != 3 {
		t.Fatal(codes(fs))
	}
}
func TestPathTokens(t *testing.T) {
	got := PathTokens([]string{"Change src/app/main.go, then edit `.gitlab-ci.yml`. Also see https://x.test/a/b and (Dockerfile)", "docs/readme.md."})
	want := map[string]bool{"src/app/main.go": true, ".gitlab-ci.yml": true, "Dockerfile": true, "docs/readme.md": true, "x.test/a/b": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Fatal(got, want)
	}
}
func TestRiskAreasAndSignals(t *testing.T) {
	sens := map[string]string{}
	low := map[string]string{"auth": "low", "payment": "low", "migration": "low", "data_deletion": "low", "ci_config": "low", "cross_service": "low"}
	cases := []struct {
		name string
		in   RiskInput
		want string
	}{
		{"plain", RiskInput{Text: []string{"Add CSV export to the invoice list. The file name contains the date."}, AreaClass: sens}, RiskLow},
		{"migration", RiskInput{Text: []string{"Migrate users table"}, AreaClass: sens}, RiskSensitive},
		{"auth fa", RiskInput{Text: []string{"بهبود احراز هویت کاربران"}, AreaClass: sens}, RiskSensitive},
		{"payment fa", RiskInput{Text: []string{"افزودن درگاه پرداخت"}, AreaClass: sens}, RiskSensitive},
		{"ci", RiskInput{Text: []string{"Cache dependencies in .gitlab-ci.yml"}, AreaClass: sens}, RiskSensitive},
		{"author is not auth", RiskInput{Text: []string{"Show the author and authority of each post"}, AreaClass: sens}, RiskLow},
		{"policy low", RiskInput{Text: []string{"Migrate users table"}, AreaClass: low}, RiskLow},
		{"policy medium", RiskInput{Text: []string{"Rotate the password policy"}, AreaClass: map[string]string{"auth": "medium"}}, RiskMedium},
		{"review path", RiskInput{Text: []string{"x"}, AreaClass: low, ReviewMatches: []string{"src/auth/a.go"}}, RiskSensitive},
		{"many items", RiskInput{Text: []string{"x"}, AreaClass: low, AcceptanceItems: 9}, RiskMedium},
		{"eight items", RiskInput{Text: []string{"x"}, AreaClass: low, AcceptanceItems: 8}, RiskLow},
		{"links", RiskInput{Text: []string{"x"}, AreaClass: low, LinkCount: 3}, RiskMedium},
		{"jev adds an area", RiskInput{Text: []string{"x"}, AreaClass: sens, JevAreas: []string{"payment"}}, RiskSensitive},
	}
	for _, c := range cases {
		if r := Risk(c.in); r.Class != c.want {
			t.Errorf("%s: %s (%v)", c.name, r.Class, r.Reasons)
		}
	}
	if r := Risk(RiskInput{Text: []string{"Migrate users and fix login"}, AreaClass: sens}); len(r.Areas) != 2 || r.Areas[0] != "auth" || r.Areas[1] != "migration" {
		t.Fatal(r.Areas)
	}
}

func answers(clarity string, pc float64, scope string, testable, done float64, areas map[string]float64) decision.Response {
	r := decision.Response{Answers: map[string]decision.Answer{
		"clarity":      {Type: "choice", Choice: clarity, Probabilities: map[string]float64{clarity: pc}},
		"scope":        {Type: "choice", Choice: scope, Probabilities: map[string]float64{scope: 0.95}},
		"testable":     {Type: "noul", Noul: testable},
		"already_done": {Type: "noul", Noul: done},
	}}
	for _, id := range []string{"touches_auth", "touches_payment", "touches_migration", "touches_data_deletion", "touches_ci_config", "cross_service"} {
		r.Answers[id] = decision.Answer{Type: "noul", Noul: areas[id]}
	}
	return r
}
func decide(in DecideInput) Decision { return Decide(in) }
func base() DecideInput {
	return DecideInput{Risk: RiskInput{Text: []string{"Add CSV export"}, AreaClass: map[string]string{}}, AcceptanceSource: "section", AcceptanceItems: 2, HasApprovedTest: true}
}
func jev(r decision.Response) *JevAnswers { j := ReadJev(r); return &j }
func TestReadJevThresholds(t *testing.T) {
	j := ReadJev(answers("clear", 0.8, "single_repo", 0.7, 0.3, map[string]float64{"touches_migration": 0.69}))
	if j.Clarity != "clear" || j.Testable != "true" || j.AlreadyDone != "false" || j.Scope != "single_repo" || j.Areas["migration"] != unsureWord || j.Areas["auth"] != "false" {
		t.Fatalf("%+v", j)
	}
	j = ReadJev(answers("clear", 0.79, "unclear", 0.5, 0.31, nil))
	if j.Clarity != unsureWord || j.Scope != unsureWord || j.Testable != unsureWord || j.AlreadyDone != unsureWord {
		t.Fatalf("%+v", j)
	}
	if j := ReadJev(decision.Response{}); j.Clarity != unsureWord || j.Testable != unsureWord {
		t.Fatal("a response without answers is all unsure")
	}
}
func TestDecideTable(t *testing.T) {
	sens := map[string]string{}
	// clear -> execute by jev
	in := base()
	in.Jev = jev(answers("clear", 0.95, "single_repo", 0.9, 0.05, nil))
	if d := decide(in); d.Outcome != OutcomeExecute || d.Source != SourceJev || d.Risk != RiskLow {
		t.Fatalf("%+v", d)
	}
	// ambiguous / contradictory / multi repo / untestable / already done stop
	for name, c := range map[string]struct {
		r      decision.Response
		reason string
		code   string
	}{
		"ambiguous":     {answers("ambiguous", 0.9, "single_repo", 0.9, 0.05, nil), ReasonAmbiguousBehavior, "ambiguous_requirements"},
		"contradictory": {answers("contradictory", 0.9, "single_repo", 0.9, 0.05, nil), ReasonAmbiguousBehavior, "contradictory_requirements"},
		"multi":         {answers("clear", 0.95, "multi_repo", 0.9, 0.05, nil), ReasonProjectMapping, "multi_repo"},
		"untestable":    {answers("clear", 0.95, "single_repo", 0.1, 0.05, nil), ReasonAmbiguousBehavior, "untestable_acceptance"},
		"done":          {answers("clear", 0.95, "single_repo", 0.9, 0.9, nil), ReasonAmbiguousBehavior, "already_addressed_confirm"},
	} {
		in := base()
		in.Jev = jev(c.r)
		d := decide(in)
		if d.Outcome != OutcomeNeedsInput || d.Reason != c.reason || d.QuestionCode != c.code || d.Source != SourceJev {
			t.Errorf("%s: %+v", name, d)
		}
	}
	// unsure + low + acceptance items + approved test -> fixed rules execute
	in = base()
	in.Jev = jev(answers("clear", 0.5, "single_repo", 0.9, 0.05, nil))
	if d := decide(in); d.Outcome != OutcomeExecute || d.Source != SourceFixedRules {
		t.Fatalf("%+v", d)
	}
	// unsure + sensitive -> Inbox
	in = base()
	in.Risk.Text = []string{"Migrate users table"}
	in.Risk.AreaClass = sens
	in.Jev = jev(answers("clear", 0.5, "single_repo", 0.9, 0.05, nil))
	if d := decide(in); d.Outcome != OutcomeNeedsInput || d.QuestionCode != "needs_clarification" || d.Risk != RiskSensitive {
		t.Fatalf("%+v", d)
	}
	// Jev saying yes to touches_migration escalates a clear task to sensitive (it still executes; the plan gate asks).
	in = base()
	in.Jev = jev(answers("clear", 0.95, "single_repo", 0.9, 0.05, map[string]float64{"touches_migration": 0.95}))
	if d := decide(in); d.Outcome != OutcomeExecute || d.Risk != RiskSensitive || !contains(d.RiskReasons, "area:migration") {
		t.Fatalf("%+v", d)
	}
	// Jev cannot clear a deterministic sensitive finding.
	in = base()
	in.Risk.Text = []string{"Migrate users table"}
	in.Risk.AreaClass = sens
	in.Jev = jev(answers("clear", 0.95, "single_repo", 0.9, 0.05, map[string]float64{"touches_migration": 0.0}))
	if d := decide(in); d.Risk != RiskSensitive {
		t.Fatalf("%+v", d)
	}
	// jev off / failed -> fixed rules; no acceptance items -> needs clarification
	in = base()
	if d := decide(in); d.Outcome != OutcomeExecute || d.Source != SourceFixedRules {
		t.Fatalf("%+v", d)
	}
	in.AcceptanceItems = 0
	if d := decide(in); d.Outcome != OutcomeNeedsInput || d.QuestionCode != "needs_clarification" {
		t.Fatalf("%+v", d)
	}
	in = base()
	in.AcceptanceSource = "none"
	if d := decide(in); d.Outcome != OutcomeNeedsInput {
		t.Fatalf("%+v", d)
	}
	// medium risk is not low enough for the fixed rules
	in = base()
	in.Risk.AcceptanceItems = 9
	if d := decide(in); d.Outcome != OutcomeNeedsInput || d.Risk != RiskMedium {
		t.Fatalf("%+v", d)
	}
	// a guard always decides first, whatever Jev says
	in = base()
	in.Findings = Guards(GuardInput{StatusCategory: "done"})
	in.Jev = jev(answers("clear", 0.95, "single_repo", 0.9, 0.05, nil))
	if d := decide(in); d.Outcome != OutcomeAlreadyAddressed || d.Source != SourceGuard {
		t.Fatalf("%+v", d)
	}
}
func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}
func TestJevStateIsScrubbedAndBounded(t *testing.T) {
	long := strings.Repeat("The report must show totals. ", 400)
	st := JevState(StateInput{Title: "Export", IssueType: "Task", Acceptance: "* a\n```\nsecret code\n```\ntoken=abc123 Bearer abcdefghijkl", Description: long, Parent: &ParentInfo{"Epic", long},
		Links: make([]LinkInfo, 25), Comments: []string{"c1", "c2", "c3", "c4", "c5", "c6 glpat-abcdefghij12345", "c7"}, Answers: []string{long}, PackagePath: "web", HasTestCommand: true})
	b, _ := json.Marshal(st)
	if utf8.RuneCount(b) > StateLimit {
		t.Fatal(utf8.RuneCount(b))
	}
	s := string(b)
	for _, leak := range []string{"abc123", "abcdefghijkl", "secret code", "glpat-"} {
		if strings.Contains(s, leak) {
			t.Fatalf("%s leaked: %s", leak, s)
		}
	}
	if len(st["links"].([]map[string]string)) != 10 || len(st["comments"].([]string)) != 5 || st["comments"].([]string)[0] != "c3" {
		t.Fatal(st["links"], st["comments"])
	}
	if !strings.Contains(s, "[code omitted]") || st["repo"].(map[string]any)["has_test_command"] != true {
		t.Fatal(s)
	}
	small := JevState(StateInput{Title: "t", Description: "d"})
	if small["description"] != "d" {
		t.Fatal(small)
	}
}
