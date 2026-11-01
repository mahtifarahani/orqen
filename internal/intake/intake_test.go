package intake

import (
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

func TestComposeJQLGolden(t *testing.T) {
	cases := []struct {
		name string
		in   JQLInput
		want string
	}{
		{"defaults", JQLInput{ProjectID: "10000", Label: "ai-ready", AssignedToMe: true}, `project = 10000 AND labels = "ai-ready" AND assignee = currentUser() AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"user jql wins over sprint and loses order by", JQLInput{ProjectID: "10000", UserJQL: ptr("issuetype = Bug OR priority = High order by created DESC"), BoardScrum: true, SprintIDs: []string{"11"}, RequireOpenSprint: true, Label: "x"}, `project = 10000 AND (issuetype = Bug OR priority = High) AND labels = "x" AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"only order by is no jql", JQLInput{ProjectID: "10000", UserJQL: ptr("ORDER BY rank")}, `project = 10000 AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"scrum sprints", JQLInput{ProjectID: "10000", BoardScrum: true, SprintIDs: []string{"11", "12", "x;drop"}, RequireOpenSprint: true, WindowMinutes: 17}, `project = 10000 AND sprint in (11, 12) AND statusCategory != Done AND updated >= -17m ORDER BY updated ASC, key ASC`},
		{"scrum fallback", JQLInput{ProjectID: "10000", BoardScrum: true, RequireOpenSprint: true}, `project = 10000 AND sprint in openSprints() AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"kanban ignores sprint", JQLInput{ProjectID: "10000", BoardScrum: false, RequireOpenSprint: true}, `project = 10000 AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"label escaping", JQLInput{ProjectID: "10000", Label: `a"b\c`}, `project = 10000 AND labels = "a\"b\\c" AND statusCategory != Done ORDER BY updated ASC, key ASC`},
		{"key project is quoted", JQLInput{ProjectID: `OR"Q`}, `project = "OR\"Q" AND statusCategory != Done ORDER BY updated ASC, key ASC`},
	}
	for _, c := range cases {
		if got := ComposeJQL(c.in); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		if !strings.HasPrefix(ComposeJQL(c.in), "project = ") {
			t.Errorf("%s: project clause is not first", c.name)
		}
	}
}
func TestWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if m, full := Window(nil, now, 0, 10); !full || m != 0 {
		t.Fatal("no success must scan fully")
	}
	last := now.Add(-2*time.Minute - 10*time.Second)
	if m, full := Window(&last, now, 3, 10); full || m != 3+OverlapMinutes {
		t.Fatal(m, full)
	}
	last = now.Add(-10 * time.Minute)
	if m, _ := Window(&last, now, 0, 10); m != 15 {
		t.Fatal(m)
	}
	if _, full := Window(&last, now, 10, 10); !full {
		t.Fatal("reconcile count must force a full scan")
	}
	if _, full := Window(&last, now, 9, 10); full {
		t.Fatal(full)
	}
	old := now.Add(-time.Duration(MaxWindowMinutes+1) * time.Minute)
	if _, full := Window(&old, now, 0, 10); !full {
		t.Fatal("a gap above one week must scan fully")
	}
	future := now.Add(time.Hour)
	if m, full := Window(&future, now, 0, 10); full || m != OverlapMinutes {
		t.Fatal("clock skew must not make a negative window", m, full)
	}
}
func TestNormalize(t *testing.T) {
	got := Normalize("  a  \r\nb\t\r\n\r\n\r\n\r\n\r\nc\rd  \n")
	if got != "a\nb\n\n\nc\nd" {
		t.Fatalf("%q", got)
	}
	if Normalize("e\u0301") == Normalize("\u00e9") {
		t.Fatal("no Unicode normalization expected")
	}
}
func TestExtractAcceptance(t *testing.T) {
	cases := []struct {
		name, desc string
		field      *string
		source     string
		items      []string
	}{
		{"wiki", "Intro\n\nh2. Acceptance Criteria\n* One thing\n* Another thing\n\nh2. Notes\n* not this", nil, SourceSection, []string{"One thing", "Another thing"}},
		{"markdown", "# Title\n## Acceptance\n- [ ] first\n- [x] second\n1. third\n## Other\n- no", nil, SourceSection, []string{"first", "second", "third"}},
		{"bold line", "Text\n*AC:*\n# numbered one\n# numbered two\n\n*Notes*\n* nope", nil, SourceSection, []string{"numbered one", "numbered two"}},
		{"definition of done", "**Definition of Done**\n* ship it", nil, SourceSection, []string{"ship it"}},
		{"persian", "توضیح\n\nh3. معیارهای پذیرش\n* دکمه خروجی CSV را دانلود می‌کند\n* نام فایل شامل تاریخ است", nil, SourceSection, []string{"دکمه خروجی CSV را دانلود می‌کند", "نام فایل شامل تاریخ است"}},
		{"persian singular markdown", "## معیار پذیرش\n- مورد اول", nil, SourceSection, []string{"مورد اول"}},
		{"field first", "h2. Acceptance\n* from description", ptr("* from field\n* second"), SourceField, []string{"from field", "second"}},
		{"field without bullets", "x", ptr("Must filter by date.\nMust sort."), SourceField, []string{"Must filter by date.", "Must sort."}},
		{"empty field falls back", "h2. AC\n* body", ptr("  "), SourceSection, []string{"body"}},
		{"none", "Make search better", nil, SourceNone, []string{}},
		{"empty section is none", "h2. Acceptance Criteria\n\nh2. Next\n* x", nil, SourceNone, []string{}},
		{"heading word inside prose is not a heading", "We accept the criteria: AC are good.\n* a bullet", nil, SourceNone, []string{}},
	}
	for _, c := range cases {
		a := ExtractAcceptance(c.desc, c.field)
		if a.Source != c.source || strings.Join(a.Items, "|") != strings.Join(c.items, "|") {
			t.Errorf("%s: %+v", c.name, a)
		}
		if a.Items == nil {
			t.Errorf("%s: items must not be nil", c.name)
		}
	}
}
func TestExtractRefs(t *testing.T) {
	r := ExtractRefs("See https://x.atlassian.net/wiki/spaces/A/pages/1, and [design|https://www.figma.com/file/abc?node-id=1].",
		"Docs: https://example.com/a. Also https://confluence.corp.local/display/ORQ/Invoices) and https://example.com/a")
	if len(r.Figma) != 1 || !strings.HasPrefix(r.Figma[0], "https://www.figma.com/file/abc") || len(r.Confluence) != 2 || len(r.URLs) != 1 || r.URLs[0] != "https://example.com/a" {
		t.Fatalf("%+v", r)
	}
}
func base() Snapshot {
	return Snapshot{Summary: "Add export", Description: "Do it\r\n", Acceptance: Acceptance{Text: "* a", Source: SourceSection}, IssueType: "Task",
		Components: []string{"b", "a"}, Labels: []string{"ai-ready", "z", "y"}, Parent: &ParentRef{"1", "P", "pd"},
		Links:       []LinkRef{{"Relates", "outward", "5", "S5", "indeterminate"}, {"Blocks", "inward", "6", "S6", "new"}},
		Attachments: []AttachmentRef{{"2", "b.png", "image/png", 5}, {"1", "a.png", "image/png", 4}},
		Comments:    []CommentRef{{"c2", "second"}, {"c1", "first"}},
		StatusName:  "To Do", StatusCategory: "new", Priority: "High", AssigneeID: "acc-1", Updated: "2026-01-01T00:00:00.000+0000"}
}
func TestMaterialFingerprint(t *testing.T) {
	b := base()
	fp := MaterialFP(b, "ai-ready")
	same := []struct {
		name string
		f    func(*Snapshot)
	}{
		{"status", func(s *Snapshot) { s.StatusName, s.StatusCategory = "In Progress", "indeterminate" }},
		{"priority", func(s *Snapshot) { s.Priority = "Low" }},
		{"assignee", func(s *Snapshot) { s.AssigneeID = "acc-2" }},
		{"updated", func(s *Snapshot) { s.Updated = "2027" }},
		{"resolution", func(s *Snapshot) { s.Resolution = "Done" }},
		{"link status", func(s *Snapshot) { s.Links[0].StatusCategory = "done" }},
		{"order of lists", func(s *Snapshot) {
			s.Components = []string{"a", "b"}
			s.Labels = []string{"y", "z", "ai-ready"}
			s.Attachments = []AttachmentRef{s.Attachments[1], s.Attachments[0]}
			s.Comments = []CommentRef{s.Comments[1], s.Comments[0]}
			s.Links = []LinkRef{s.Links[1], s.Links[0]}
		}},
		{"line endings", func(s *Snapshot) { s.Description = "Do it\n\n\n\n\n" }},
		{"intake label removed", func(s *Snapshot) { s.Labels = []string{"z", "y"} }},
		{"orqen comment", func(s *Snapshot) {
			s.Comments = append(s.Comments, CommentRef{"c9", "Draft MR opened " + OrqenMarker + "run_1"})
		}},
	}
	for _, c := range same {
		s := base()
		c.f(&s)
		if MaterialFP(s, "ai-ready") != fp {
			t.Errorf("%s must not be material", c.name)
		}
	}
	diff := []struct {
		name string
		f    func(*Snapshot)
	}{
		{"summary", func(s *Snapshot) { s.Summary = "Add exports" }},
		{"description", func(s *Snapshot) { s.Description = "Do it now" }},
		{"acceptance", func(s *Snapshot) { s.Acceptance.Text = "* b" }},
		{"acceptance source", func(s *Snapshot) { s.Acceptance.Source = SourceField }},
		{"type", func(s *Snapshot) { s.IssueType = "Bug" }},
		{"component", func(s *Snapshot) { s.Components = []string{"a"} }},
		{"other label", func(s *Snapshot) { s.Labels = append(s.Labels, "new") }},
		{"parent", func(s *Snapshot) { s.Parent.Description = "changed" }},
		{"no parent", func(s *Snapshot) { s.Parent = nil }},
		{"link", func(s *Snapshot) { s.Links = s.Links[:1] }},
		{"attachment", func(s *Snapshot) { s.Attachments[0].Size++ }},
		{"comment", func(s *Snapshot) { s.Comments[0].Body = "edited" }},
		{"new comment", func(s *Snapshot) { s.Comments = append(s.Comments, CommentRef{"c3", "hi"}) }},
	}
	for _, c := range diff {
		s := base()
		c.f(&s)
		if MaterialFP(s, "ai-ready") == fp {
			t.Errorf("%s must be material", c.name)
		}
	}
	// ContentHash sees volatile fields.
	s := base()
	s.Priority = "Low"
	if ContentHash(s) == ContentHash(base()) {
		t.Fatal("content hash must include priority")
	}
	s = base()
	s.Comments = append(s.Comments, CommentRef{"c9", OrqenMarker + "r"})
	if ContentHash(s) == ContentHash(base()) {
		t.Fatal("content hash must include marker comments")
	}
}
func TestIntentKeys(t *testing.T) {
	pi := PolicyIntent("prj_1", "org:con:1", "main")
	if pi == PolicyIntent("prj_1", "org:con:1", "develop") || pi == PolicyIntent("prj_2", "org:con:1", "main") {
		t.Fatal("policy intent must depend on project, repo and branch")
	}
	k := IntentKey("org_1", "con_1", "10", "fp", pi)
	if k != IntentKey("org_1", "con_1", "10", "fp", pi) || k == IntentKey("org_2", "con_1", "10", "fp", pi) || k == IntentKey("org_1", "con_1", "10", "fp2", pi) || k == IntentKey("org_1", "con_1", "11", "fp", pi) {
		t.Fatal("intent key")
	}
	if len(k) != 64 {
		t.Fatal(k)
	}
}
