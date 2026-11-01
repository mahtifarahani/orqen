package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"io"
	"net/http"
	"net/http/httptest"
	"orqen/api"
	"orqen/internal/catherd"
	"orqen/internal/readiness"
	"orqen/web"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestIDsAndTokens(t *testing.T) {
	for _, prefix := range []string{"org", "con", "cred", "job", "op", "art", "sess", "idem", "tok"} {
		id := newID(prefix)
		if !validID(prefix, id) || validID("wrong", id) || len(id) != len(prefix)+27 {
			t.Fatal(id)
		}
	}
	for _, v := range []string{"", "org_short", "org_ILOU0000000000000000000000", "orgX01ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		if validID("org", v) {
			t.Fatal(v)
		}
	}
	raw := randomToken()
	if len(raw) != 43 || raw == randomToken() || len(sha256Hex(raw)) != 64 {
		t.Fatal("token invariant")
	}
}
func TestConfig(t *testing.T) {
	c := DefaultConfig()
	if contains(c.AllowedOrigins(), "http://localhost:5173") {
		t.Fatal("dev origin enabled")
	}
	c.Dev = true
	if !contains(c.AllowedOrigins(), "http://localhost:5173") || !contains(c.AllowedHosts(), "localhost:5173") {
		t.Fatal("dev origin missing")
	}
	t.Setenv("ORQEN_PORT", "5001")
	got, cmd, err := ParseConfig([]string{"--data-dir", "/tmp/test", "serve", "--port", "5002", "--dev"})
	if err != nil || cmd != "serve" || got.Port != 5002 || !got.Dev {
		t.Fatal(got, cmd, err)
	}
	for _, args := range [][]string{{"serve", "--lease-ttl-secs", "0"}, {"serve", "--worker-concurrency", "0"}, {"serve", "--credential-store", "bad"}, {"serve", "--unknown"}, {"serve", "junk"}} {
		if _, _, err := ParseConfig(args); err == nil {
			t.Fatal(args)
		}
	}
}
func TestNormalization(t *testing.T) {
	for raw, want := range map[string]string{"https://Jira.Corp.local:8443/jira/": "https://jira.corp.local:8443/jira", "https://git.x/gitlab": "https://git.x/gitlab", "http://a:80/": "http://a", "https://a:443": "https://a", "http://127.0.0.1:4280/Case/Kept": "http://127.0.0.1:4280/Case/Kept", "http://[::1]:80/": "http://[::1]"} {
		n, e := normalizeURL(raw)
		if e != nil || n.URL != want {
			t.Fatal(raw, n, e)
		}
	}
	for _, raw := range []string{"https://u:p@x", "https://x/?a=1", "ftp://x", "https://x/#f", "not a url", "https://", "https://x?", "https://x#"} {
		if _, e := normalizeURL(raw); e == nil {
			t.Fatal(raw)
		}
	}
}
func TestValidation(t *testing.T) {
	valid := CreateOrgRequest{"abc", "  سلام  ", "fa", "Asia/Tehran"}
	if e := validateOrg(valid, true); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"-abc", "abc-", "ab", "ABC", "a/b", strings.Repeat("a", 41)} {
		v := valid
		v.Slug = s
		if validateOrg(v, true) == nil {
			t.Fatal(s)
		}
	}
	req := CreateConnectionRequest{Name: "  Jira  ", Provider: "jira", Deployment: "server", BaseURL: "https://X/jira/", AuthMethod: "personal_access_token"}
	d, e := checkConnection(req)
	if e != nil || d.Name != "Jira" || d.BaseURL != "https://x/jira" || d.TimeoutMS != 15000 || !reflect.DeepEqual(d.AllowedHosts, []string{"x"}) {
		t.Fatal(d, e)
	}
	for _, change := range []func(*CreateConnectionRequest){func(r *CreateConnectionRequest) { r.TimeoutMS = ptr(5) }, func(r *CreateConnectionRequest) { r.MaxConcurrency = ptr(33) }, func(r *CreateConnectionRequest) { r.Provider = "bad" }, func(r *CreateConnectionRequest) { r.ProxyURL = ptr("http://user:pw@x") }, func(r *CreateConnectionRequest) { r.CustomCAPEM = ptr("invalid") }, func(r *CreateConnectionRequest) { r.AllowedHosts = ptr([]string{}) }} {
		v := req
		change(&v)
		if _, e = checkConnection(v); e == nil {
			t.Fatal(v)
		}
	}
}
func TestPreconditions(t *testing.T) {
	for raw, want := range map[string]int64{"\"3\"": 3, "3": 3, "W/\"7\"": 7} {
		r := httptest.NewRequest("PATCH", "/", nil)
		r.Header.Set("If-Match", raw)
		got, e := ifMatch(r)
		if e != nil || got != want {
			t.Fatal(raw, got, e)
		}
	}
	for _, v := range []string{"abc", "\"0\"", "", "-1"} {
		r := httptest.NewRequest("PATCH", "/", nil)
		r.Header.Set("If-Match", v)
		if _, e := ifMatch(r); e == nil {
			t.Fatal(v)
		}
	}
	for _, v := range []string{"", "has space", strings.Repeat("a", 256), "é"} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Idempotency-Key", v)
		if _, e := idempotencyKey(r, false); e == nil {
			t.Fatal(v)
		}
	}
}
func TestHashAndRedaction(t *testing.T) {
	a := requestHash("POST", "/x/{id}", map[string]string{"b": "2", "a": "1"}, bson.M{"y": 2, "x": 1})
	b := requestHash("POST", "/x/{id}", map[string]string{"a": "1", "b": "2"}, bson.M{"x": 1, "y": 2})
	if a != b || a == requestHash("PUT", "/x/{id}", map[string]string{"a": "1", "b": "2"}, bson.M{"x": 1, "y": 2}) {
		t.Fatal("canonical hash")
	}
	if canonicalJSON(map[string]any{"x": "<tag>&سلام"}) != `{"x":"<tag>&سلام"}` {
		t.Fatal("JSON escaped differently than Rust")
	}
	m := redact(bson.M{"api_token": "x", "credential_ref": "ref", "nested": bson.M{"Authorization": "secret", "ok": 1}, "list": []any{bson.M{"password": "x"}}}).(map[string]any)
	if m["api_token"] != "[REDACTED]" || m["credential_ref"] != "ref" || m["nested"].(map[string]any)["Authorization"] != "[REDACTED]" {
		t.Fatal(m)
	}
}
func TestBackoffAndOverall(t *testing.T) {
	for attempt, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 3: 8 * time.Second, 20: 300 * time.Second} {
		if backoffWith(attempt, 1) != want {
			t.Fatal(attempt)
		}
	}
	if overallStatus([]StageResult{stage("network", "passed", "", ""), stage("auth", "not_implemented", "", "")}) != "reachable" || overallStatus([]StageResult{stage("credential", "failed", "", "")}) != "degraded" || overallStatus([]StageResult{stage("http", "failed", "", "")}) != "unreachable" {
		t.Fatal("overall")
	}
}
func TestArtifactStorage(t *testing.T) {
	root := t.TempDir()
	org := newID("org")
	blob, e := storeBlob(root, org, strings.NewReader("abc"), 3)
	if e != nil || blob.SHA256 != sha256Hex("abc") {
		t.Fatal(blob, e)
	}
	same, e := storeBlob(root, org, strings.NewReader("abc"), 3)
	if e != nil || same != blob {
		t.Fatal(same, e)
	}
	if _, e = storeBlob(root, org, strings.NewReader("abcd"), 3); asAppError(e).Code != "payload_too_large" {
		t.Fatal(e)
	}
	files, _ := os.ReadDir(filepath.Join(root, ".tmp"))
	if len(files) != 0 {
		t.Fatal(files)
	}
	if _, e = artifactPath(root, org, "../../etc/passwd"); e == nil {
		t.Fatal("unsafe digest")
	}
	if _, e = artifactPath(root, "../x", blob.SHA256); e == nil {
		t.Fatal("unsafe org")
	}
}
func TestUI(t *testing.T) {
	for _, path := range []string{"/", "/orgs/x/connections", "/assets/missing.js"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		web.ServeUI(w, r, false)
		if w.Code == 503 {
			t.Skip("web UI not yet built")
		}
		if path == "/assets/missing.js" {
			if w.Code != 404 {
				t.Fatal(w.Code)
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatal(w.Code, w.Header())
		}
	}
}
func TestProblemSanitizationAndHost(t *testing.T) {
	b := errorBody(errors.New("secret /path"), "req_test")
	if strings.Contains(b["detail"].(string), "secret") {
		t.Fatal(b)
	}
	a := NewApp(DefaultConfig(), nil, NewMemoryStore())
	defer a.Stop()
	r := httptest.NewRequest("GET", "http://evil.example:4280/api/docs/", nil)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 421 || w.Header().Get("X-Request-ID") == "" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatal(w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "http://localhost:4280/api/no-such-route", nil)
	w = httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestContract(t *testing.T) {
	var spec struct {
		OpenAPI    string                    `json:"openapi"`
		Paths      map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(api.OpenAPI, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatal(spec.OpenAPI)
	}
	a := NewApp(DefaultConfig(), nil, NewMemoryStore())
	defer a.Stop()
	count := 0
	for _, routes := range spec.Paths {
		for method := range routes {
			if contains([]string{"get", "post", "put", "patch", "delete"}, method) {
				count++
			}
		}
	}
	if count != len(a.Routes()) {
		t.Fatalf("contract has %d routes, server has %d", count, len(a.Routes()))
	}
	for _, r := range a.Routes() {
		if _, ok := spec.Paths[r.Path][strings.ToLower(r.Method)]; !ok {
			t.Fatal(r.Method, r.Path)
		}
	}
	for name, value := range map[string]any{"CreateOrgRequest": CreateOrgRequest{}, "PatchOrgRequest": PatchOrgRequest{}, "CreateConnectionRequest": CreateConnectionRequest{}, "PatchConnectionRequest": PatchConnectionRequest{}, "PatchProjectRequest": PatchProjectRequest{}, "FromCatalogRequest": FromCatalogRequest{}, "RepoMappingInput": RepoMappingInput{}, "JiraBoardInput": JiraBoardInput{}, "JiraMappingInput": JiraMappingInput{}, "ConfluenceRefInput": ConfluenceRefInput{}, "MappingInput": MappingInput{}, "EnvSourceInput": EnvSourceInput{}, "EnvVarInput": EnvVarInput{}, "CommandOriginInput": CommandOriginInput{}, "CommandInput": CommandInput{}, "PutCommandsRequest": PutCommandsRequest{}, "ProtectedPathInput": ProtectedPathInput{}, "PutProtectedPathsRequest": PutProtectedPathsRequest{}, "RepoMapping": RepoMapping{}, "JiraBoardRef": JiraBoardRef{}, "JiraMapping": JiraMapping{}, "ConfluenceRef": ConfluenceRef{}, "Mapping": Mapping{}, "CheckResult": CheckResult{}, "EnvSource": EnvSource{}, "EnvVar": EnvVar{}, "CommandOrigin": CommandOrigin{}, "CommandSpec": CommandSpec{}, "ProtectedPath": ProtectedPath{}, "ContractFile": ContractFile{}, "CommandSuggestion": CommandSuggestion{}, "PutPolicyDraftRequest": PutPolicyDraftRequest{}, "DoctorCheck": DoctorCheck{}, "CatherdCatalogRequest": CatherdCatalogRequest{}, "PreflightCheck": PreflightCheck{}, "PatchLaunchdRequest": PatchLaunchdRequest{}, "ProfileMeta": ProfileMeta{}, "CatherdIssue": catherd.Issue{}, "Independence": catherd.Independence{}, "ValidationRecordDto": ValidationRecord{}, "VersionFailure": VersionFailure{}, "ProfileFrom": ProfileFrom{}, "CreateProfileRequest": CreateProfileRequest{}, "PatchProfileRequest": PatchProfileRequest{}, "PutDraftRequest": PutDraftRequest{}, "RollbackRequest": RollbackRequest{}, "ImportPersonalRequest": ImportPersonalRequest{}, "BindingRequest": BindingRequest{}, "PatchSetupRequest": PatchSetupRequest{}, "SkipStepRequest": SkipStepRequest{}, "PutCatherdSecretRequest": PutCatherdSecretRequest{}, "RunDoctorRequest": RunDoctorRequest{}, "PutAutomationRequest": PutAutomationRequest{}, "CreatePinnedRuleRequest": CreatePinnedRuleRequest{}, "PatchPinnedRuleRequest": PatchPinnedRuleRequest{}, "AnswerInboxRequest": AnswerInboxRequest{}, "ResolveInboxRequest": ResolveInboxRequest{}, "CancelRunRequest": CancelRunRequest{}} {
		typ := reflect.TypeOf(value)
		props := spec.Components.Schemas[name].Properties
		if len(props) != typ.NumField() {
			t.Fatal("schema field count", name, len(props), typ.NumField())
		}
		for i := 0; i < typ.NumField(); i++ {
			field := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if _, ok := props[field]; !ok {
				t.Fatal(name, field)
			}
		}
	}
	for name, dto := range map[string]bson.M{"OrganizationDto": (OrganizationDoc{}).DTO(), "ConnectionDto": (ConnectionDoc{}).DTO(), "OperationDto": (OperationDoc{}).DTO(), "ArtifactDto": (ArtifactDoc{}).DTO(), "EventDto": (EventDoc{}).DTO(), "CatalogProjectDto": (CatalogProjectDoc{}).DTO(), "CatalogSummaryDto": (CatalogSummaryItem{}).DTO(), "ProjectDto": (ProjectDoc{}).DTO(), "ProjectSummaryDto": (ProjectSummaryDoc{}).DTO(), "ProjectReadinessDto": readinessDTO(readiness.Result{}), "ContractSnapshotDto": (ContractSnapshotDoc{}).DTO(), "PolicyDraftDto": (PolicyDraftDoc{}).DTO(), "PolicyVersionDto": (PolicyVersionDoc{}).DTO(), "ModelProfileDto": (ModelProfileDoc{}).DTO(), "ModelProfileVersionDto": (ModelProfileVersionDoc{}).DTO(), "SetupWizardDto": (WizardDoc{}).DTO(), "CatherdSecretDto": (CatherdSecretDoc{}).DTO(), "TaskSummaryDto": (TrackedIssueDoc{}).SummaryDTO(nil, nil), "TaskDto": (TrackedIssueDoc{}).TaskDTO(IssueSnapshotDoc{}, false, nil, nil, nil), "IssueSnapshotDto": (IssueSnapshotDoc{}).DTO(), "RunSummaryDto": (RunDoc{}).SummaryDTO(nil), "RunDto": (RunDoc{}).DTO(), "WorkspaceDto": (WorkspaceDoc{}).DTO(), "RunAttemptDto": (RunAttemptDoc{}).DTO(), "IntakeStateDto": (IntakeStateDoc{}).DTO(false, false, "manual"), "AutomationStateDto": (AutomationStateDoc{}).DTO(), "PinnedRuleDto": (PinnedRuleDoc{}).DTO(), "InboxItemDto": (InboxItemDoc{}).DTO()} {
		props := spec.Components.Schemas[name].Properties
		for field := range dto {
			if _, ok := props[field]; !ok {
				t.Fatal(name, field)
			}
		}
		for field := range props {
			if field == "deduplicated" {
				continue
			}
			if _, ok := dto[field]; !ok {
				t.Fatal(name, "missing", field)
			}
		}
	}
}
func TestProbeHTTPAndTLS(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://outside.invalid/", 302)
		default:
			w.WriteHeader(401)
		}
	}))
	defer up.Close()
	n, _ := normalizeURL(up.URL)
	conn := ConnectionDoc{BaseURL: up.URL + "/context", TimeoutMS: 1000, AllowedHosts: []string{n.Host}}
	if r := httpStage(context.Background(), conn, "http"); r.Status != "passed" || r.Detail != "HTTP 401" {
		t.Fatal(r)
	}
	conn.BaseURL = up.URL + "/redirect"
	if r := httpStage(context.Background(), conn, "http"); r.Code == nil || *r.Code != "redirect_out_of_scope" {
		t.Fatal(r)
	}
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer tlsUp.Close()
	conn.BaseURL = tlsUp.URL
	if r := httpStage(context.Background(), conn, "tls"); r.Status != "failed" || r.Code == nil || *r.Code != "tls_error" {
		t.Fatal(r)
	}
}
func TestKeychain(t *testing.T) {
	if os.Getenv("ORQEN_TEST_KEYCHAIN") != "1" {
		t.Skip("set ORQEN_TEST_KEYCHAIN=1 to exercise the OS credential store")
	}
	s := KeychainStore{}
	ref := newID("cred")
	ctx := context.Background()
	if err := s.Put(ctx, ref, "test-secret"); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(ctx, ref)
	v, ok, err := s.Get(ctx, ref)
	if err != nil || !ok || v != "test-secret" {
		t.Fatal(ok, err)
	}
	if err = s.Delete(ctx, ref); err != nil {
		t.Fatal(err)
	}
	_, ok, err = s.Get(ctx, ref)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}
func TestBSONCompatibility(t *testing.T) {
	raw, err := bson.Marshal(bson.M{"_id": newID("con"), "credential_updated_at": nil, "last_test": nil, "timeout_ms": int64(15000), "max_concurrency": int32(4), "created_at": bson.NewDateTimeFromTime(time.Now())})
	if err != nil {
		t.Fatal(err)
	}
	var d ConnectionDoc
	if err = bson.Unmarshal(raw, &d); err != nil || d.TimeoutMS != 15000 || d.MaxConcurrency != 4 || d.CredentialUpdatedAt != nil {
		t.Fatal(d, err)
	}
	encoded, err := json.Marshal(d.DTO())
	if err != nil || bytes.Contains(encoded, []byte("credential_ref")) {
		t.Fatal(err, string(encoded))
	}
}
func TestMalformedJSON(t *testing.T) {
	for _, body := range []string{`{`, `{"secret":"x","unknown":1}`, `{} {}`} {
		r := httptest.NewRequest("PUT", "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		var target struct {
			Secret string `json:"secret"`
		}
		if err := decodeJSON(w, r, &target, true); err == nil {
			t.Fatal(body)
		}
	}
	r := httptest.NewRequest("PUT", "/", io.NopCloser(strings.NewReader(strings.Repeat("x", 1024*1024+1))))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	var v any
	if err := decodeJSON(w, r, &v, true); err == nil {
		t.Fatal("malformed body accepted")
	}
}

func TestContractRefs(t *testing.T) {
	var spec map[string]any
	if err := json.Unmarshal(api.OpenAPI, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if r, ok := x["$ref"].(string); ok {
				name, found := strings.CutPrefix(r, "#/components/schemas/")
				if _, exists := schemas[name]; !found || !exists {
					t.Errorf("unresolved reference %s", r)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(spec)
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	ids := map[string]string{}
	declared := map[string]bool{}
	for path, item := range spec["paths"].(map[string]any) {
		for method, raw := range item.(map[string]any) {
			op := raw.(map[string]any)
			key := strings.ToUpper(method) + " " + path
			id, _ := op["operationId"].(string)
			if !snake.MatchString(id) || ids[id] != "" {
				t.Errorf("operationId %q of %s must be unique snake_case (also %s)", id, key, ids[id])
			}
			ids[id] = key
			headers := map[string]bool{}
			required := map[string]bool{}
			params, _ := op["parameters"].([]any)
			for _, p := range params {
				param := p.(map[string]any)
				if param["in"] == "header" {
					headers[param["name"].(string)] = true
					required[param["name"].(string)] = param["required"] == true
				}
			}
			public := false
			sec, _ := op["security"].([]any)
			for _, s := range sec {
				if len(s.(map[string]any)) == 0 {
					public = true
				}
			}
			if method != "get" && !public && !headers["X-CSRF-Token"] {
				t.Errorf("%s does not declare X-CSRF-Token", key)
			}
			declared[key] = required["Idempotency-Key"]
		}
	}
	a := NewApp(DefaultConfig(), nil, NewMemoryStore())
	defer a.Stop()
	routes := map[string]bool{}
	for _, r := range a.Routes() {
		key := r.Method + " " + r.Path
		routes[key] = true
		if declared[key] != idemRequired[key] {
			t.Errorf("%s: contract requires Idempotency-Key=%v, idemRequired=%v", key, declared[key], idemRequired[key])
		}
	}
	for key := range idemRequired {
		if !routes[key] {
			t.Errorf("idemRequired names unknown route %s", key)
		}
	}
}
