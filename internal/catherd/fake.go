package catherd

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed testdata/default-profile.json
var defaultProfileJSON []byte

//go:embed testdata/catalog-small.json
var fakeCatalogJSON []byte

// Call records one Port call for assertions. Env lists the injected secret names with sha256(value)[:8], for
// example "TYPESAFE_API_KEY=1a2b3c4d"; the values themselves are never recorded.
type Call struct{ Op, Home, Name, Key, Host, Env string }

// EnvNames returns the injected secret names of the call.
func (c Call) EnvNames() []string {
	var out []string
	for _, p := range strings.Split(c.Env, ",") {
		if p != "" {
			out = append(out, strings.SplitN(p, "=", 2)[0])
		}
	}
	return out
}

// SecretHash is the fingerprint the fake records for a secret value.
func SecretHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])[:8]
}
func envRecord(sec SecretEnv) string {
	names := make([]string, 0, len(sec))
	for k := range sec {
		names = append(names, k)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, k := range names {
		parts = append(parts, k+"="+SecretHash(sec[k]))
	}
	return strings.Join(parts, ",")
}

// Fake is an in-memory Port that mirrors merge-patch semantics, strict patch keys and minimal validation.
type Fake struct {
	mu       sync.Mutex
	homes    map[string]map[string]map[string]any
	calls    []Call
	failNext map[string]error
	crash    int
	armed    bool
	sets     int

	VersionString string
	CatalogJSON   []byte
	DoctorJSON    json.RawMessage
	DoctorExit    int
	// Personal profiles served to PersonalEnv reads.
	Personal map[string]map[string]any
	// AfterSet runs after each successful set (crash and race hooks).
	AfterSet func(home, name string)
	// Runs serves the run side (RunsShow, RunsCancel, ProfileUseRepo). Without it those calls fail with
	// orqen_unavailable; internal/testfakes/hostfake provides one.
	Runs RunsPort
	// Bound records profile bindings made through ProfileUseRepo when Runs is nil: repoDir -> profile name.
	bound map[string]string
}

func NewFake() *Fake {
	return &Fake{homes: map[string]map[string]map[string]any{}, failNext: map[string]error{}, VersionString: "1.5.0", CatalogJSON: fakeCatalogJSON, Personal: map[string]map[string]any{},
		DoctorJSON: json.RawMessage(`{"host":"codex","queue":"idle","push":"n/a","ready":true,"version":"1.5.0","checks":[{"id":"config","label":"config","state":"ok","word":"ok","detail":""},{"id":"backend:codex","label":"codex","state":"ok","word":"ok","detail":""},{"id":"jev","label":"jev","state":"warn","word":"no key","detail":""}]}`)}
}

// FailNext makes the next call of op (version, list, show_raw, show_resolved, create, set, validate, remove, catalog_list, doctor, ...) fail.
func (f *Fake) FailNext(op string, err error) { f.mu.Lock(); f.failNext[op] = err; f.mu.Unlock() }

// CrashAfterSets lets n sets succeed and then makes the next set fail with a plain (retryable) error.
func (f *Fake) CrashAfterSets(n int) {
	f.mu.Lock()
	f.crash, f.armed, f.sets = n, true, 0
	f.mu.Unlock()
}
func (f *Fake) Calls() []Call { f.mu.Lock(); defer f.mu.Unlock(); return append([]Call{}, f.calls...) }
func (f *Fake) CallsOf(op string) []Call {
	var out []Call
	for _, c := range f.Calls() {
		if c.Op == op {
			out = append(out, c)
		}
	}
	return out
}

// Names lists the profiles in a home.
func (f *Fake) Names(home string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.homes[home] {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Doc returns a copy of a stored profile document.
func (f *Fake) Doc(home, name string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.homes[home][name]; ok {
		return Clone(d).(map[string]any)
	}
	return nil
}

// Put stores a document directly (test setup, for example a half-built profile).
func (f *Fake) Put(home, name string, doc map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.homes[home] == nil {
		f.homes[home] = map[string]map[string]any{}
	}
	d := Clone(doc).(map[string]any)
	d["name"] = name
	f.homes[home][name] = d
}
func (f *Fake) begin(op string, env ReadEnv, host, name, key string, sec SecretEnv) error {
	home := ""
	if env != nil {
		home = env.Home()
	}
	for k := range sec {
		if !has(AllowedSecretEnv, k) {
			panic("catherd: " + k + " is not an injectable secret")
		}
	}
	f.calls = append(f.calls, Call{op, home, name, key, host, envRecord(sec)})
	if host != "" && !ValidHost(host) {
		return &Error{Code: CodeRejected, Message: "unknown orchestration host", Exit: 2}
	}
	if err, ok := f.failNext[op]; ok {
		delete(f.failNext, op)
		return err
	}
	return nil
}
func (f *Fake) Version(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("version", nil, "", "", "", nil); err != nil {
		return "", err
	}
	return f.VersionString, nil
}
func (f *Fake) ListProfiles(_ context.Context, env ReadEnv) ([]ProfileRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("list", env, "", "", "", nil); err != nil {
		return nil, err
	}
	var names []string
	if _, personal := env.(*PersonalEnv); personal {
		for n := range f.Personal {
			names = append(names, n)
		}
	} else {
		if f.homes[env.Home()] == nil {
			f.homes[env.Home()] = map[string]map[string]any{}
		}
		for n := range f.homes[env.Home()] {
			names = append(names, n)
		}
		if _, ok := f.homes[env.Home()]["default"]; !ok {
			names = append(names, "default")
		}
	}
	sort.Strings(names)
	rows := []ProfileRow{}
	for _, n := range names {
		rows = append(rows, ProfileRow{Name: n, Active: n == "default", Repos: []string{}})
	}
	return rows, nil
}
func (f *Fake) defaultDoc(name string) map[string]any {
	d, _ := Decode(defaultProfileJSON)
	d["name"] = name
	return d
}
func (f *Fake) get(env ReadEnv, name string) (map[string]any, error) {
	if _, personal := env.(*PersonalEnv); personal {
		if d, ok := f.Personal[name]; ok {
			return Clone(d).(map[string]any), nil
		}
		return nil, &Error{Code: "E_NOT_FOUND", Message: "no profile " + name, Exit: 1}
	}
	if d, ok := f.homes[env.Home()][name]; ok {
		return d, nil
	}
	if name == "default" {
		return f.defaultDoc("default"), nil
	}
	return nil, &Error{Code: "E_NOT_FOUND", Message: "no profile " + name, Exit: 1}
}
func (f *Fake) ShowRaw(_ context.Context, env ReadEnv, host, name string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("show_raw", env, host, name, "", nil); err != nil {
		return nil, err
	}
	d, err := f.get(env, name)
	if err != nil {
		return nil, err
	}
	return Clone(d).(map[string]any), nil
}
func (f *Fake) ShowResolved(_ context.Context, env *OrgEnv, host, name string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("show_resolved", env, host, name, "", nil); err != nil {
		return nil, err
	}
	d, err := f.get(env, name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"profile": Clone(d), "active": false, "enforcement": map[string]any{}, "standIns": map[string]any{}}, nil
}
func (f *Fake) Create(_ context.Context, env *OrgEnv, host, name string, sec SecretEnv) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("create", env, host, name, "", sec); err != nil {
		return err
	}
	if !NamePattern.MatchString(name) {
		return &Error{Code: "E_INPUT_INVALID", Message: "bad profile name", Exit: 2}
	}
	if f.homes[env.Home()] == nil {
		f.homes[env.Home()] = map[string]map[string]any{}
	}
	if _, ok := f.homes[env.Home()][name]; ok {
		return &Error{Code: "E_EXISTS", Message: "profile exists", Exit: 1}
	}
	f.homes[env.Home()][name] = f.defaultDoc(name)
	return nil
}
func (f *Fake) catalog() Catalog { c, _ := ParseCatalog(f.CatalogJSON); return c }

// check is the fake's minimal validation: worker enabled, rung grammar, rungs in the catalog, no native claude on codex.
func (f *Fake) check(doc map[string]any, host string) []Issue {
	var out []Issue
	roles, _ := doc["roles"].(map[string]any)
	cat := f.catalog()
	if w, ok := roles["worker"].(map[string]any); ok && w["enabled"] == false {
		out = append(out, NewIssue("catherd", "catherd_invalid", []string{"roles", "worker", "enabled"}, "the worker cannot be disabled"))
	}
	names := make([]string, 0, len(roles))
	for r := range roles {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, role := range names {
		rm, _ := roles[role].(map[string]any)
		rungs, _ := rm["rungs"].([]any)
		for i, x := range rungs {
			s, _ := x.(string)
			p := []string{"roles", role, "rungs", fmt.Sprint(i)}
			rg, ok := ParseRung(s)
			switch {
			case !ok:
				out = append(out, NewIssue("catherd", "catherd_invalid", p, "bad rung "+s))
			case rg.Backend == "claude" && host != HostClaudeCode:
				out = append(out, NewIssue("catherd", "catherd_invalid", p, "native claude rungs are refused on the codex host"))
			default:
				if _, cr, found := cat.Lookup(s); !found || !cr.Enabled && cr.TreatLike == nil {
					out = append(out, NewIssue("catherd", "catherd_invalid", p, "unscored rung "+s))
				}
			}
		}
	}
	return out
}
func (f *Fake) SetKey(_ context.Context, env *OrgEnv, host, name, key string, value any, sec SecretEnv) (SetOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("set", env, host, name, key, sec); err != nil {
		return SetOutcome{}, err
	}
	if f.armed && f.sets >= f.crash {
		f.armed = false
		return SetOutcome{}, errors.New("injected crash")
	}
	doc, ok := f.homes[env.Home()][name]
	if !ok {
		return SetOutcome{}, &Error{Code: "E_NOT_FOUND", Message: "no profile " + name, Exit: 1}
	}
	// Round-trip the value so numbers are json.Number like the real CLI's JSON.
	b, _ := json.Marshal(value)
	v, err := Decode([]byte(`{"v":` + string(b) + `}`))
	if err != nil {
		return SetOutcome{}, &Error{Code: CodeRejected, Message: "bad value", Exit: 2}
	}
	patch := map[string]any{key: v["v"]}
	if issues := ValidatePatch(patch); len(issues) > 0 {
		return SetOutcome{}, &Error{Code: CodeRejected, Message: "cannot set " + key + ": " + issues[0].Message, Exit: 2}
	}
	next := MergePatch(doc, patch).(map[string]any)
	if issues := f.check(next, host); len(issues) > 0 {
		for i := range issues {
			issues[i].Code = "catherd_refused"
		}
		return SetOutcome{Refused: issues}, nil
	}
	f.homes[env.Home()][name] = next
	f.sets++
	if f.AfterSet != nil {
		f.mu.Unlock()
		f.AfterSet(env.Home(), name)
		f.mu.Lock()
	}
	return SetOutcome{Saved: true}, nil
}
func (f *Fake) Validate(_ context.Context, env *OrgEnv, host, name string, sec SecretEnv) (Validation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("validate", env, host, name, "", sec); err != nil {
		return Validation{}, err
	}
	doc, err := f.get(env, name)
	if err != nil {
		return Validation{}, err
	}
	errs := f.check(doc, host)
	return Validation{Valid: len(errs) == 0, Errors: append([]Issue{}, errs...), Warnings: []Issue{}}, nil
}
func (f *Fake) Remove(_ context.Context, env *OrgEnv, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("remove", env, "", name, "", nil); err != nil {
		return err
	}
	if _, ok := f.homes[env.Home()][name]; !ok {
		return &Error{Code: "E_NOT_FOUND", Message: "no profile " + name, Exit: 1}
	}
	delete(f.homes[env.Home()], name)
	return nil
}
func (f *Fake) CatalogList(_ context.Context, env *OrgEnv) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("catalog_list", env, "", "", "", nil); err != nil {
		return nil, err
	}
	return f.CatalogJSON, nil
}
func (f *Fake) CatalogRefresh(_ context.Context, env *OrgEnv, sec SecretEnv) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("catalog_refresh", env, "", "", "", sec); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"refreshed":true}`), nil
}
func (f *Fake) CatalogSync(_ context.Context, env *OrgEnv, force bool, sec SecretEnv) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("catalog_sync", env, "", "", fmt.Sprint(force), sec); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"synced":true}`), nil
}
func (f *Fake) Doctor(_ context.Context, env *OrgEnv, host string, sec SecretEnv) (json.RawMessage, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.begin("doctor", env, host, "", "", sec); err != nil {
		return nil, 0, err
	}
	// The report names the host it ran for.
	var m map[string]json.RawMessage
	if json.Unmarshal(f.DoctorJSON, &m) == nil {
		m["host"], _ = json.Marshal(host)
		if b, err := json.Marshal(m); err == nil {
			return b, f.DoctorExit, nil
		}
	}
	return f.DoctorJSON, f.DoctorExit, nil
}

func (f *Fake) noRuns() error {
	return &Error{Code: CodeUnavailable, Message: "the fake has no run backend"}
}

// RunsShow delegates to the run backend.
func (f *Fake) RunsShow(ctx context.Context, env *OrgEnv, runID string) (RunView, error) {
	f.mu.Lock()
	err := f.begin("runs_show", env, "", runID, "", nil)
	f.mu.Unlock()
	if err != nil {
		return RunView{}, err
	}
	if f.Runs == nil {
		return RunView{}, f.noRuns()
	}
	return f.Runs.RunsShow(ctx, env, runID)
}

// RunsCancel delegates to the run backend.
func (f *Fake) RunsCancel(ctx context.Context, env *OrgEnv, runID, name string) error {
	f.mu.Lock()
	err := f.begin("runs_cancel", env, "", runID+"/"+name, "", nil)
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if f.Runs == nil {
		return f.noRuns()
	}
	return f.Runs.RunsCancel(ctx, env, runID, name)
}

// ProfileUseRepo records the binding and delegates when a run backend is present.
func (f *Fake) ProfileUseRepo(ctx context.Context, env *OrgEnv, host, name, repoDir string) error {
	f.mu.Lock()
	err := f.begin("profile_use_repo", env, host, name, repoDir, nil)
	if err == nil {
		if f.bound == nil {
			f.bound = map[string]string{}
		}
		f.bound[repoDir] = name
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if f.Runs != nil {
		return f.Runs.ProfileUseRepo(ctx, env, host, name, repoDir)
	}
	return nil
}

// BoundProfile is the profile ProfileUseRepo bound to a repo directory.
func (f *Fake) BoundProfile(repoDir string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bound[repoDir]
}

var _ Port = (*Fake)(nil)

// fakeState is the part of a Fake that cmd/orqen-fakehost keeps on disk between catherd CLI invocations.
type fakeState struct {
	Homes map[string]map[string]map[string]any `json:"homes"`
	Bound map[string]string                    `json:"bound"`
}

// ExportState serializes the profiles and bindings (for the file-backed CLI emulation).
func (f *Fake) ExportState() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(fakeState{Homes: f.homes, Bound: f.bound})
}

// ImportState loads what ExportState wrote. Numbers stay json.Number like the real CLI's JSON.
func (f *Fake) ImportState(b []byte) error {
	var raw struct {
		Homes map[string]map[string]json.RawMessage `json:"homes"`
		Bound map[string]string                     `json:"bound"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.homes, f.bound = map[string]map[string]map[string]any{}, raw.Bound
	for home, docs := range raw.Homes {
		f.homes[home] = map[string]map[string]any{}
		for name, d := range docs {
			m, err := Decode(d)
			if err != nil {
				return err
			}
			f.homes[home][name] = m
		}
	}
	return nil
}
