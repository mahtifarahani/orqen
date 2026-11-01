package providers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"orqen/internal/providers"
	"orqen/internal/testfakes"
	"strings"
	"testing"
)

func jiraFake(cloud bool) (*testfakes.Jira, *httptest.Server, providers.IssueSource) {
	fake := testfakes.NewJira()
	fake.Cloud = cloud
	fake.SeedIssues()
	srv := httptest.NewServer(fake.Handler())
	if cloud {
		return fake, srv, providers.NewJira(opts(srv.URL+"/jira"), providers.Auth{Method: "api_token", Username: fake.User, Secret: fake.Token}, "cloud")
	}
	return fake, srv, providers.NewJira(opts(srv.URL+"/jira"), providers.Auth{Method: "personal_access_token", Secret: fake.Token}, "data_center")
}

const eligible = `project = 10000 AND labels = "ai-ready" AND assignee = currentUser() AND statusCategory != Done ORDER BY updated ASC, key ASC`

func TestSearchPathsAndPaging(t *testing.T) {
	for _, cloud := range []bool{true, false} {
		fake, srv, src := jiraFake(cloud)
		ctx := context.Background()
		var keys []string
		page, pages := "", 0
		for {
			p, err := src.SearchIssues(ctx, eligible, []string{"summary", "status", "labels", "assignee", "issuetype", "priority", "updated"}, page, 2)
			if err != nil {
				t.Fatal(cloud, err)
			}
			pages++
			for _, i := range p.Items {
				keys = append(keys, i.Key)
				if i.Summary == "" || i.StatusCategory == "" || i.AssigneeID != "acc-1" || len(i.Labels) != 1 {
					t.Fatal(cloud, i)
				}
			}
			if p.Next == "" {
				break
			}
			page = p.Next
		}
		// ORQ-1,2,3,4,6,7 are eligible (ORQ-9 is Done, ORQ-8 unlabeled); order is updated ASC then key.
		if strings.Join(keys, ",") != "ORQ-1,ORQ-2,ORQ-3,ORQ-4,ORQ-6,ORQ-7" || pages != 3 {
			t.Fatal(cloud, keys, pages)
		}
		want := "/jira/rest/api/2/search"
		if cloud {
			want = "/jira/rest/api/3/search/jql"
		}
		for _, r := range fake.Requests() {
			if r != want {
				t.Fatal(cloud, "unexpected request", r)
			}
		}
		if len(fake.JQLs()) != 3 || fake.JQLs()[0] != eligible {
			t.Fatal(fake.JQLs())
		}
		srv.Close()
	}
}
func TestSearchWindowAndSprintClauses(t *testing.T) {
	fake, srv, src := jiraFake(false)
	defer srv.Close()
	ctx := context.Background()
	p, err := src.SearchIssues(ctx, `project = ORQ AND updated >= -30m`, []string{"summary"}, "", 50)
	if err != nil || len(p.Items) != 0 {
		t.Fatal(p, err)
	}
	fake.SetIssue("ORQ-2", func(i *testfakes.FakeIssue) { i.Summary = "Make search better (edited)" })
	p, err = src.SearchIssues(ctx, `project = ORQ AND updated >= -30m`, []string{"summary"}, "", 50)
	if err != nil || len(p.Items) != 1 || p.Items[0].Key != "ORQ-2" {
		t.Fatal(p, err)
	}
	p, err = src.SearchIssues(ctx, `project = ORQ AND (issuetype = Epic)`, nil, "", 50)
	if err != nil || len(p.Items) != 2 {
		t.Fatal(p, err)
	}
	p, err = src.SearchIssues(ctx, `project = ORQ AND sprint in (11) AND labels = "ai-ready"`, nil, "", 50)
	if err != nil || len(p.Items) != 7 {
		t.Fatal(len(p.Items), err)
	}
	if _, err = src.SearchIssues(ctx, "BROKEN", nil, "", 10); kindOf(t, err).Code != "jql_invalid" {
		t.Fatal(err)
	}
}
func TestGetIssueAndComments(t *testing.T) {
	_, srv, src := jiraFake(true)
	defer srv.Close()
	ctx := context.Background()
	is, err := src.GetIssue(ctx, "ORQ-1", []string{"summary", "description", "issuetype", "status", "priority", "labels", "components", "assignee", "parent", "issuelinks", "attachment", "resolution", "updated"})
	if err != nil {
		t.Fatal(err)
	}
	if is.Key != "ORQ-1" || is.ID != "20001" || is.DescriptionADF || !strings.Contains(is.Description, "Acceptance Criteria") || is.Priority != "High" || is.IssueType != "Task" || is.StatusCategory != "new" {
		t.Fatal(is)
	}
	if is.Parent == nil || is.Parent.Key != "ORQ-10" || is.Parent.Summary != "Invoice improvements" || len(is.Components) != 1 || is.Components[0] != "billing" {
		t.Fatal(is.Parent, is.Components)
	}
	if len(is.Links) != 1 || is.Links[0].Key != "ORQ-5" || is.Links[0].Direction != "outward" || is.Links[0].StatusCategory != "indeterminate" {
		t.Fatal(is.Links)
	}
	if len(is.Attachments) != 1 || is.Attachments[0].Filename != "mock.png" || is.Attachments[0].Size != 2048 {
		t.Fatal(is.Attachments)
	}
	blocked, err := src.GetIssue(ctx, "ORQ-4", nil)
	if err != nil || len(blocked.Links) != 1 || blocked.Links[0].Direction != "inward" || blocked.Links[0].Type != "Blocks" {
		t.Fatal(blocked.Links, err)
	}
	done, err := src.GetIssue(ctx, "20009", nil)
	if err != nil || done.Resolution != "Done" || done.StatusCategory != "done" {
		t.Fatal(done, err)
	}
	cp, err := src.ListComments(ctx, "ORQ-1", 0, 1)
	if err != nil || len(cp.Items) != 1 || cp.NextStartAt != 1 || cp.Total != 2 || cp.Items[0].AuthorDisplay != "Dana" {
		t.Fatal(cp, err)
	}
	cp, err = src.ListComments(ctx, "ORQ-1", 1, 5)
	if err != nil || len(cp.Items) != 1 || cp.NextStartAt != -1 || !strings.Contains(cp.Items[0].Body, "ignore previous instructions") {
		t.Fatal(cp, err)
	}
}
func TestADFDescriptionFallback(t *testing.T) {
	fake, srv, src := jiraFake(true)
	defer srv.Close()
	fake.SetIssue("ORQ-3", func(i *testfakes.FakeIssue) { i.ADF = true; i.Description = "line one\nline two" })
	is, err := src.GetIssue(context.Background(), "ORQ-3", []string{"description"})
	if err != nil || !is.DescriptionADF || is.Description != "line one\nline two" {
		t.Fatalf("%q %v %v", is.Description, is.DescriptionADF, err)
	}
	if txt, adf := providers.TextOf(json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"x"}]}]}]}]}`)); !adf || txt != "ab\n- x" {
		t.Fatalf("%q", txt)
	}
	if txt, adf := providers.TextOf(json.RawMessage(`"plain"`)); adf || txt != "plain" {
		t.Fatal(txt)
	}
}
func TestCustomFieldIsReturnedRaw(t *testing.T) {
	fake, srv, src := jiraFake(false)
	defer srv.Close()
	fake.SetIssue("ORQ-2", func(i *testfakes.FakeIssue) {
		i.Fields = map[string]string{"customfield_10100": "It must filter by date."}
	})
	is, err := src.GetIssue(context.Background(), "ORQ-2", []string{"summary", "customfield_10100"})
	if err != nil {
		t.Fatal(err)
	}
	if txt, _ := providers.TextOf(is.Fields["customfield_10100"]); txt != "It must filter by date." {
		t.Fatal(is.Fields)
	}
}
func TestIssueErrorsMapToKinds(t *testing.T) {
	fake, srv, src := jiraFake(false)
	defer srv.Close()
	ctx := context.Background()
	if _, err := src.GetIssue(ctx, "ORQ-404", nil); kindOf(t, err).Kind != providers.KindNotFound {
		t.Fatal(err)
	}
	fake.Fail["/rest/api/2/issue/ORQ-2"] = &testfakes.Failure{Status: 403, Count: 1}
	if _, err := src.GetIssue(ctx, "ORQ-2", nil); kindOf(t, err).Kind != providers.KindPermissionDenied {
		t.Fatal(err)
	}
	fake.Fail["/rest/api/2/search"] = &testfakes.Failure{Status: 429, RetryAfter: "9", Count: 1}
	_, err := src.SearchIssues(ctx, "project = ORQ", nil, "", 10)
	if e := kindOf(t, err); e.Kind != providers.KindRateLimited || e.RetryAfter.Seconds() != 9 {
		t.Fatal(e)
	}
	fake.Fail["/rest/api/2/issue/ORQ-1/comment"] = &testfakes.Failure{Status: 503, Count: 1}
	if _, err = src.ListComments(ctx, "ORQ-1", 0, 5); !kindOf(t, err).Retryable() {
		t.Fatal(err)
	}
	fake.SetToken("rotated")
	if _, err = src.SearchIssues(ctx, "project = ORQ", nil, "", 10); kindOf(t, err).Kind != providers.KindCredentialExpired {
		t.Fatal(err)
	}
}
func TestSprintsAndAgileFallback(t *testing.T) {
	fake, srv, src := jiraFake(false)
	defer srv.Close()
	ctx := context.Background()
	sp, err := src.ActiveSprints(ctx, "1")
	if err != nil || len(sp) != 1 || sp[0].ID != "11" || sp[0].Goal == "" {
		t.Fatal(sp, err)
	}
	fake.AgileMissing = true
	if _, err = src.ActiveSprints(ctx, "1"); kindOf(t, err).Kind != providers.KindUnsupported {
		t.Fatal(err)
	}
}
func TestIssueRequestsKeepThePrefix(t *testing.T) {
	fake, srv, src := jiraFake(true)
	defer srv.Close()
	ctx := context.Background()
	_, _ = src.SearchIssues(ctx, "project = ORQ", nil, "", 5)
	_, _ = src.GetIssue(ctx, "ORQ-1", nil)
	_, _ = src.ListComments(ctx, "ORQ-1", 0, 5)
	_, _ = src.ActiveSprints(ctx, "1")
	for _, r := range fake.Requests() {
		if !strings.HasPrefix(r, "/jira/rest/") {
			t.Fatal(r)
		}
	}
}
func TestGitLabProjectCarriesCloneURL(t *testing.T) {
	fake := testfakes.NewGitLab()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	fake.PublicBase = srv.URL
	host := providers.NewGitLab(opts(srv.URL+"/gitlab"), glAuth())
	p, err := host.GetProject(context.Background(), "1")
	if err != nil || p.HTTPURLToRepo != srv.URL+"/gitlab/group-1/proj-1.git" {
		t.Fatal(p, err)
	}
	var _ http.Handler = fake.Handler()
}
