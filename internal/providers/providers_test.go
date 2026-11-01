package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"orqen/internal/providers"
	"orqen/internal/testfakes"
	"strings"
	"sync"
	"testing"
	"time"
)

func glAuth() providers.Auth {
	return providers.Auth{Provider: "gitlab", Method: "personal_access_token", Secret: "glpat-test"}
}
func opts(base string) providers.Options {
	return providers.Options{BaseURL: base, Timeout: 3 * time.Second}
}
func kindOf(t *testing.T, err error) *providers.Error {
	t.Helper()
	e, ok := providers.AsError(err)
	if !ok {
		t.Fatalf("not a provider error: %v", err)
	}
	return e
}

func TestAuthMatrix(t *testing.T) {
	cases := []struct {
		name         string
		auth         providers.Auth
		header, want string
		code         string
	}{
		{"gitlab pat", providers.Auth{Provider: "gitlab", Method: "personal_access_token", Secret: "s"}, "PRIVATE-TOKEN", "s", ""},
		{"gitlab api_token", providers.Auth{Provider: "gitlab", Method: "api_token", Secret: "s"}, "PRIVATE-TOKEN", "s", ""},
		{"gitlab oauth", providers.Auth{Provider: "gitlab", Method: "oauth", Secret: "s"}, "Authorization", "Bearer s", ""},
		{"gitlab basic", providers.Auth{Provider: "gitlab", Method: "basic", Secret: "s"}, "", "", "auth_method_unsupported"},
		{"cloud api_token", providers.Auth{Provider: "jira", Deployment: "cloud", Method: "api_token", Username: "a@b", Secret: "s"}, "Authorization", "Basic YUBiOnM=", ""},
		{"cloud no username", providers.Auth{Provider: "jira", Deployment: "cloud", Method: "api_token", Secret: "s"}, "", "", "username_required"},
		{"cloud pat", providers.Auth{Provider: "jira", Deployment: "cloud", Method: "personal_access_token", Secret: "s"}, "", "", "auth_method_unsupported"},
		{"server pat", providers.Auth{Provider: "jira", Deployment: "server", Method: "personal_access_token", Secret: "s"}, "Authorization", "Bearer s", ""},
		{"dc basic", providers.Auth{Provider: "jira", Deployment: "data_center", Method: "basic", Username: "a@b", Secret: "s"}, "Authorization", "Basic YUBiOnM=", ""},
		{"dc oauth", providers.Auth{Provider: "jira", Deployment: "data_center", Method: "oauth", Secret: "s"}, "Authorization", "Bearer s", ""},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", "http://x", nil)
		err := c.auth.Apply(req)
		if c.code != "" {
			if e, ok := providers.AsError(err); !ok || e.Code != c.code {
				t.Fatal(c.name, err)
			}
			continue
		}
		if err != nil || req.Header.Get(c.header) != c.want {
			t.Fatal(c.name, err, req.Header)
		}
	}
}
func TestGitLabAgainstFakeUnderPrefix(t *testing.T) {
	fake := testfakes.NewGitLab()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	host := providers.NewGitLab(opts(srv.URL+"/gitlab"), glAuth())
	ctx := context.Background()
	id, err := host.Identity(ctx)
	if err != nil || id.Username != "orqen-bot" || id.ProviderUserID != "42" {
		t.Fatal(id, err)
	}
	caps, err := host.Capabilities(ctx)
	if err != nil || caps.ServerVersion != "17.0.0" || caps.Features["write_api"] != "available" || caps.Features["list_projects"] != "available" {
		t.Fatal(caps, err)
	}
	seen := map[string]bool{}
	cursor, pages := "", 0
	for {
		page, err := host.ListProjectsPage(ctx, cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, p := range page.Items {
			if seen[p.ID] {
				t.Fatal("duplicate", p.ID)
			}
			seen[p.ID] = true
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(seen) != 250 || pages != 3 {
		t.Fatal(len(seen), pages)
	}
	p, err := host.GetProject(ctx, "7")
	if err != nil || !p.Archived || p.PathWithNamespace != "group-1/proj-7" {
		t.Fatal(p, err)
	}
	if bs, err := host.ListBranches(ctx, "1", "dev", 10); err != nil || len(bs) != 1 || bs[0].Name != "develop" {
		t.Fatal(bs, err)
	}
	if _, err = host.GetBranch(ctx, "1", "nope"); kindOf(t, err).Kind != providers.KindNotFound {
		t.Fatal(err)
	}
	sha, err := host.ResolveCommit(ctx, "1", "main")
	if err != nil || len(sha) != 40 {
		t.Fatal(sha, err)
	}
	tree, err := host.ListTree(ctx, "1", sha, "")
	if err != nil || len(tree) != 4 {
		t.Fatal(tree, err)
	}
	data, truncated, err := host.ReadFileRaw(ctx, "1", sha, "package.json", 1<<18)
	if err != nil || truncated || !strings.Contains(string(data), `"scripts"`) {
		t.Fatal(string(data), truncated, err)
	}
	data, truncated, err = host.ReadFileRaw(ctx, "1", sha, "package.json", 10)
	if err != nil || !truncated || len(data) != 10 {
		t.Fatal(len(data), truncated, err)
	}
	for _, r := range fake.Requests() {
		if !strings.HasPrefix(r, "/gitlab/api/v4/") {
			t.Fatal("request outside the configured prefix:", r)
		}
	}
}
func TestGitLabOffsetFallback(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.Offset = true
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	host := providers.NewGitLab(opts(srv.URL+"/gitlab"), glAuth())
	n, cursor := 0, ""
	for i := 0; i < 10; i++ {
		page, err := host.ListProjectsPage(context.Background(), cursor)
		if err != nil {
			t.Fatal(err)
		}
		n += len(page.Items)
		if page.Next == "" {
			break
		}
		if !strings.HasPrefix(page.Next, "page:") {
			t.Fatal(page.Next)
		}
		cursor = page.Next
	}
	if n != 250 {
		t.Fatal(n)
	}
}
func TestGitLabLinkMustStayOnTheAPI(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.ForeignLink = true
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	host := providers.NewGitLab(opts(srv.URL+"/gitlab"), glAuth())
	_, err := host.ListProjectsPage(context.Background(), "")
	if e := kindOf(t, err); e.Kind != providers.KindInvalidOutput || e.Code != "invalid_next_link" {
		t.Fatal(e)
	}
	_, err = host.ListProjectsPage(context.Background(), srv.URL+"/other/api/v4/projects")
	if kindOf(t, err).Code != "invalid_next_link" {
		t.Fatal(err)
	}
	_, err = host.ListProjectsPage(context.Background(), "http://127.0.0.1:1/gitlab/api/v4/projects")
	if kindOf(t, err).Code != "invalid_next_link" {
		t.Fatal(err)
	}
}
func TestStatusMapping(t *testing.T) {
	fake := testfakes.NewGitLab()
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	bad := providers.NewGitLab(opts(srv.URL+"/gitlab"), providers.Auth{Provider: "gitlab", Method: "personal_access_token", Secret: "wrong"})
	if _, err := bad.Identity(context.Background()); kindOf(t, err).Code != "credential_rejected" || kindOf(t, err).Kind != providers.KindCredentialExpired {
		t.Fatal(err)
	}
	host := providers.NewGitLab(opts(srv.URL+"/gitlab"), glAuth())
	fake.Fail["projects/1"] = &testfakes.Failure{Status: 403, Count: 1}
	if _, err := host.GetProject(context.Background(), "1"); kindOf(t, err).Kind != providers.KindPermissionDenied {
		t.Fatal(err)
	}
	if _, err := host.GetProject(context.Background(), "999"); kindOf(t, err).Kind != providers.KindNotFound {
		t.Fatal(err)
	}
	fake.Fail["projects/2"] = &testfakes.Failure{Status: 429, RetryAfter: "7", Count: 1}
	_, err := host.GetProject(context.Background(), "2")
	if e := kindOf(t, err); e.Kind != providers.KindRateLimited || e.RetryAfter != 7*time.Second || !e.Retryable() {
		t.Fatal(e)
	}
	fake.Fail["projects/3"] = &testfakes.Failure{Status: 503, Count: 1}
	if _, err = host.GetProject(context.Background(), "3"); !kindOf(t, err).Retryable() {
		t.Fatal(err)
	}
	if _, err = providers.NewGitLab(opts("http://127.0.0.1:1/gitlab"), glAuth()).Identity(context.Background()); kindOf(t, err).Kind != providers.KindNetwork {
		t.Fatal(err)
	}
	if _, err = providers.NewGitLab(opts(srv.URL+"/gitlab"), providers.Auth{Provider: "gitlab", Method: "basic", Secret: "x"}).Identity(context.Background()); kindOf(t, err).Code != "auth_method_unsupported" {
		t.Fatal(err)
	}
}
func TestRedirectsAreNeverFollowed(t *testing.T) {
	var outside int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outside++ }))
	defer target.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/user", http.StatusFound)
	}))
	defer up.Close()
	_, err := providers.NewGitLab(opts(up.URL), glAuth()).Identity(context.Background())
	if e := kindOf(t, err); e.Code != "unexpected_redirect" || e.Kind != providers.KindInvalidOutput {
		t.Fatal(e)
	}
	if outside != 0 {
		t.Fatal("the redirect was followed")
	}
}
func TestInvalidOutput(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) }))
	defer up.Close()
	_, err := providers.NewGitLab(opts(up.URL), glAuth()).Identity(context.Background())
	if kindOf(t, err).Code != "invalid_output" {
		t.Fatal(err)
	}
}
func TestJiraCloudAndServer(t *testing.T) {
	fake := testfakes.NewJira()
	fake.Cloud = true
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	ctx := context.Background()
	cloud := providers.NewJira(opts(srv.URL+"/jira"), providers.Auth{Method: "api_token", Username: fake.User, Secret: fake.Token}, "cloud")
	id, err := cloud.Identity(ctx)
	if err != nil || id.ProviderUserID != "acc-1" {
		t.Fatal(id, err)
	}
	page, err := cloud.ListProjects(ctx, "", 0, 2)
	if err != nil || len(page.Items) != 2 || page.NextStartAt != 2 {
		t.Fatal(page, err)
	}
	page, err = cloud.ListProjects(ctx, "", 2, 2)
	if err != nil || len(page.Items) != 1 || page.NextStartAt != -1 {
		t.Fatal(page, err)
	}
	if err = cloud.CheckJQL(ctx, "project = ORQ"); err != nil {
		t.Fatal(err)
	}
	if err = cloud.CheckJQL(ctx, "BROKEN"); kindOf(t, err).Code != "jql_invalid" {
		t.Fatal(err)
	}
	if _, err = providers.NewJira(opts(srv.URL+"/jira"), providers.Auth{Method: "api_token", Secret: fake.Token}, "cloud").Identity(ctx); kindOf(t, err).Code != "username_required" {
		t.Fatal(err)
	}
	fake.Cloud = false
	dc := providers.NewJira(opts(srv.URL+"/jira"), providers.Auth{Method: "personal_access_token", Secret: fake.Token}, "data_center")
	if page, err = dc.ListProjects(ctx, "web", 0, 10); err != nil || len(page.Items) != 1 || page.Items[0].Key != "WEB" {
		t.Fatal(page, err)
	}
	boards, err := dc.ListBoards(ctx, "10000", 0, 10)
	if err != nil || len(boards.Items) != 2 || boards.Items[0].ProjectID != "10000" {
		t.Fatal(boards, err)
	}
	if b, err := dc.GetBoard(ctx, "3"); err != nil || b.ProjectID != "10001" {
		t.Fatal(b, err)
	}
	caps, err := dc.Capabilities(ctx)
	if err != nil || caps.Features["boards"] != "available" || caps.Features["browse_projects"] != "available" || caps.Features["jql_search"] != "available" {
		t.Fatal(caps, err)
	}
	fake.AgileMissing = true
	caps, err = dc.Capabilities(ctx)
	if err != nil || caps.Features["boards"] != "unsupported" {
		t.Fatal(caps, err)
	}
	if _, err = dc.ListBoards(ctx, "10000", 0, 10); kindOf(t, err).Kind != providers.KindUnsupported {
		t.Fatal(err)
	}
	for _, r := range fake.Requests() {
		if !strings.HasPrefix(r, "/jira/rest/") {
			t.Fatal("request outside the configured prefix:", r)
		}
	}
}
func TestConcurrencyGate(t *testing.T) {
	fake := testfakes.NewGitLab()
	fake.Delay = 30 * time.Millisecond
	srv := httptest.NewServer(fake.Handler())
	defer srv.Close()
	gate := make(chan struct{}, 1)
	o := opts(srv.URL + "/gitlab")
	o.Acquire = func(ctx context.Context) (func(), error) {
		select {
		case gate <- struct{}{}:
			return func() { <-gate }, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	host := providers.NewGitLab(o, glAuth())
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := host.GetProject(context.Background(), "1"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if fake.MaxInflight() != 1 {
		t.Fatal("max in flight", fake.MaxInflight())
	}
}
func TestSecretNeverInErrors(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer up.Close()
	_, err := providers.NewGitLab(opts(up.URL), providers.Auth{Provider: "gitlab", Method: "personal_access_token", Secret: "super-secret-value"}).Identity(context.Background())
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatal(err)
	}
}
