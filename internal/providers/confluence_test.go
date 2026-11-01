package providers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"orqen/internal/providers"
	"orqen/internal/testfakes"
)

func confServer(t *testing.T, cloud bool) (*testfakes.Confluence, string) {
	t.Helper()
	f := testfakes.NewConfluenceServer("/confluence")
	if cloud {
		f = testfakes.NewConfluenceCloud("/cloud")
	}
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func serverSource(t *testing.T) (providers.DocsSource, *testfakes.Confluence) {
	f, base := confServer(t, false)
	return providers.NewConfluence(providers.Options{BaseURL: base + "/confluence"}, providers.Auth{Method: "personal_access_token", Secret: f.Token}, "server", 0), f
}

func cloudSource(t *testing.T, baseSuffix string) (providers.DocsSource, *testfakes.Confluence) {
	f, base := confServer(t, true)
	return providers.NewConfluence(providers.Options{BaseURL: base + baseSuffix}, providers.Auth{Method: "api_token", Username: f.User, Secret: f.Token}, "cloud", 0), f
}

func TestConfluenceServerContextPathAndVersions(t *testing.T) {
	src, f := serverSource(t)
	ctx := context.Background()
	id, err := src.Identity(ctx)
	if err != nil || id.Username != "bot" {
		t.Fatalf("%v %+v", err, id)
	}
	p, err := src.GetPage(ctx, "1001")
	if err != nil || p.Version != 1 || p.SpaceKey != "DEV" || !strings.HasSuffix(p.URL, "/spaces/DEV/pages/1001") {
		t.Fatalf("%v %+v", err, p)
	}
	for _, want := range []string{"# Payment architecture", "- Retries use exponential backoff", "Component | Owner", "go test ./...", "```", "PostgreSQL"} {
		if !strings.Contains(p.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, p.Text)
		}
	}
	if strings.Contains(p.Text, "internal macro text") {
		t.Error("non-code macros are dropped")
	}
	if len(p.Links) != 1 || p.Links[0] != "page:Runbook" {
		t.Errorf("links %v", p.Links)
	}
	f.SetPage("1001", "", "<p>second</p>")
	p2, err := src.GetPage(ctx, "1001")
	if err != nil || p2.Version != 2 || p2.ContentHash == p.ContentHash {
		t.Fatalf("%v %+v", err, p2)
	}
	old, err := src.GetPageVersion(ctx, "1001", 1)
	if err != nil || old.ContentHash != p.ContentHash || old.RawHash != p.RawHash {
		t.Fatalf("historical read: %v", err)
	}
	hits, err := src.Search(ctx, `title ~ "release"`, 5)
	if err != nil || len(hits) != 1 || hits[0].ID != "1002" {
		t.Fatalf("%v %+v", err, hits)
	}
	// Exact path preservation: the context path appears in every request, nothing else does.
	paths := f.Requests()
	for _, p := range paths {
		if !strings.HasPrefix(p, "/confluence/rest/api/") {
			t.Errorf("path %q lost the context path or left the API", p)
		}
	}
	want := map[string]bool{"/confluence/rest/api/user/current": true, "/confluence/rest/api/content/1001": true, "/confluence/rest/api/content/search": true}
	for _, p := range paths {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Errorf("not requested: %v (got %v)", want, paths)
	}
}

func TestConfluenceCloudWikiSuffixRule(t *testing.T) {
	for _, suffix := range []string{"", "/wiki"} {
		src, f := cloudSource(t, "/cloud"+suffix)
		if suffix == "" {
			// /cloud + /wiki is added by the adapter; with /cloud/wiki given it must not be doubled
		}
		ctx := context.Background()
		if _, err := src.Identity(ctx); err != nil {
			t.Fatalf("suffix %q: %v", suffix, err)
		}
		p, err := src.GetPage(ctx, "1001")
		if err != nil || p.Version != 1 || p.SpaceKey != "DEV" {
			t.Fatalf("suffix %q: %v %+v", suffix, err, p)
		}
		f.SetPage("1001", "", "<p>v2</p>")
		v1, err := src.GetPageVersion(ctx, "1001", 1)
		if err != nil || v1.ContentHash != p.ContentHash {
			t.Fatalf("suffix %q version: %v", suffix, err)
		}
		hits, err := src.Search(ctx, `title ~ "architecture"`, 5)
		if err != nil || len(hits) != 1 || hits[0].ID != "1001" {
			t.Fatalf("suffix %q search: %v %+v", suffix, err, hits)
		}
		for _, p := range f.Requests() {
			if !strings.HasPrefix(p, "/cloud/wiki/") || strings.Contains(p, "/wiki/wiki") {
				t.Errorf("suffix %q: bad path %q", suffix, p)
			}
		}
	}
}

func TestConfluenceErrorsAndLimits(t *testing.T) {
	src, f := serverSource(t)
	ctx := context.Background()
	f.SetFail("/rest/api/content/1001", &testfakes.Failure{Status: 429, RetryAfter: "7", Count: 1})
	_, err := src.GetPage(ctx, "1001")
	if e, ok := providers.AsError(err); !ok || e.Kind != providers.KindRateLimited || e.RetryAfter.Seconds() != 7 || !e.Retryable() {
		t.Fatalf("429: %v", err)
	}
	for status, kind := range map[int]providers.Kind{401: providers.KindCredentialExpired, 403: providers.KindPermissionDenied, 503: providers.KindNetwork} {
		f.SetFail("/rest/api/content/1001", &testfakes.Failure{Status: status, Count: 1})
		if _, err = src.GetPage(ctx, "1001"); err == nil {
			t.Fatalf("status %d: no error", status)
		} else if e, _ := providers.AsError(err); e.Kind != kind {
			t.Errorf("status %d: kind %s want %s", status, e.Kind, kind)
		}
	}
	if _, err = src.GetPage(ctx, "9999"); err == nil {
		t.Fatal("unknown page")
	} else if e, _ := providers.AsError(err); e.Kind != providers.KindNotFound {
		t.Fatal(err)
	}
	if _, err = src.GetPageVersion(ctx, "1001", 9); err == nil {
		t.Fatal("unknown version")
	}
	// Wrong token.
	_, base := confServer(t, false)
	bad := providers.NewConfluence(providers.Options{BaseURL: base + "/confluence"}, providers.Auth{Method: "personal_access_token", Secret: "nope"}, "server", 0)
	if _, err = bad.Identity(ctx); err == nil {
		t.Fatal("bad token")
	} else if e, _ := providers.AsError(err); e.Kind != providers.KindCredentialExpired {
		t.Fatal(err)
	}
	// Page cap truncates visibly.
	f2, base2 := confServer(t, false)
	small := providers.NewConfluence(providers.Options{BaseURL: base2 + "/confluence"}, providers.Auth{Method: "personal_access_token", Secret: f2.Token}, "server", 120)
	p, err := small.GetPage(ctx, "1001")
	if err != nil || !p.Truncated || len(p.Text) > 121 {
		t.Fatalf("%v truncated=%v len=%d", err, p.Truncated, len(p.Text))
	}
	// Methods are GET only: the fake rejects anything else, and the interface has no write method.
	req, _ := http.NewRequest(http.MethodPost, base+"/confluence/rest/api/content/1001", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("POST must not be served: %d", resp.StatusCode)
	}
}

func TestStorageToTextAndHash(t *testing.T) {
	text, links := providers.ConfluenceStorageToText(`<h2>Title</h2><p>a&nbsp;b &amp; <a href="https://x.example/y">link</a></p><ol><li>one<ul><li>nested</li></ul></li></ol>` +
		`<ri:url ri:value="https://z.example/"/><table><tr><td>c1</td><td>c2</td></tr></table>`)
	for _, want := range []string{"## Title", "&", "- one", "  - nested", "c1 | c2"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if len(links) != 2 {
		t.Errorf("links %v", links)
	}
	if got, _ := providers.ConfluenceStorageToText(""); strings.TrimSpace(got) != "" {
		t.Error("empty")
	}
	// The normalize hash is stable across CRLF: CRLF and LF variants of the same storage hash alike.
	f := testfakes.NewConfluenceServer("/c")
	srv := httptest.NewServer(f.Handler())
	defer srv.Close()
	src := providers.NewConfluence(providers.Options{BaseURL: srv.URL + "/c"}, providers.Auth{Method: "personal_access_token", Secret: f.Token}, "server", 0)
	f.SetPage("2000", "T", "<p>line one</p>\n<p>line two</p>")
	a, _ := src.GetPage(context.Background(), "2000")
	f.SetPage("2000", "T", "<p>line one</p>\r\n<p>line two</p>")
	b, _ := src.GetPage(context.Background(), "2000")
	if a.ContentHash != b.ContentHash || a.RawHash == b.RawHash {
		t.Fatalf("normalized hash must ignore CRLF (raw hash must not): %s %s", a.ContentHash, b.ContentHash)
	}
}
