package sysprobe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	out   map[string]string
	exit  map[string]int
	calls [][]string
}

func (f *fakeRunner) Run(_ context.Context, argv []string, _ []string) (string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, argv)
	f.mu.Unlock()
	key := filepath.Base(argv[0]) + " " + strings.Join(argv[1:], " ")
	if out, ok := f.out[key]; ok {
		return out, f.exit[key], nil
	}
	return "", -1, errors.New("not found")
}

type fakeFS struct {
	mu       sync.Mutex
	dirs     []string
	failDir  string
	free     uint64
	existing map[string]bool
}

func (f *fakeFS) MkdirAll(p string, _ os.FileMode) error {
	f.mu.Lock()
	f.dirs = append(f.dirs, p)
	f.mu.Unlock()
	if p == f.failDir {
		return errors.New("permission denied")
	}
	return nil
}
func (f *fakeFS) WriteProbe(string) error          { return nil }
func (f *fakeFS) FreeBytes(string) (uint64, error) { return f.free, nil }
func (f *fakeFS) Exists(p string) bool             { return f.existing[p] }

type fakeNet struct{ results map[string]NetResult }

func (f fakeNet) Probe(_ context.Context, t ConnTarget) NetResult { return f.results[t.ID] }

func lookup(names ...string) func(string, string) string {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(_, name string) string {
		if set[name] {
			return "/bin/" + name
		}
		return ""
	}
}
func byID(cs []Check) map[string]Check {
	m := map[string]Check{}
	for _, c := range cs {
		m[c.ID] = c
	}
	return m
}
func baseInputs() Inputs {
	return Inputs{Mongo: MongoState{OK: true, SetName: "rs", Members: 1}, CredentialStore: "memory", CatherdBin: "/bin/catherd", CatherdVersion: "1.5.0", CatherdSupported: true, DataDir: "/d", Artifacts: "/d/artifacts", CatherdHome: "/d/catherd", Workspaces: "/d/workspaces", ChildPath: "/bin", UserHome: "/home/u"}
}
func happy() (Deps, *fakeRunner) {
	r := &fakeRunner{out: map[string]string{
		"bun --version": "1.4.2\n", "codex --version": "codex-cli 0.159.2\n", "codex login status": "Logged in\n", "git --version": "git version 2.39.0\n", "node --version": "v22.13.1\n",
		"claude --version": "2.1.282 (Claude Code)\n", "claude auth status --json": `{"loggedIn":true}`, "docker version --format {{.Server.Version}}": "27.0\n", "docker context show": "orbstack\n",
	}, exit: map[string]int{}}
	return Deps{Runner: r, FS: &fakeFS{free: 100 << 30, existing: map[string]bool{"/Applications/OrbStack.app": true}}, Net: fakeNet{}, LookPath: lookup("bun", "codex", "claude", "git", "node", "docker"), Getenv: func(k string) string {
		return map[string]string{"HTTPS_PROXY": "http://user:pw@proxy.corp:3128", "NO_PROXY": "localhost"}[k]
	}}, r
}
func TestHappyPath(t *testing.T) {
	d, _ := happy()
	got := byID(Run(context.Background(), d, baseInputs()))
	for id, want := range map[string]string{"mongo.replica_set": Pass, "bin.bun": Pass, "bin.codex": Pass, "bin.claude": Pass, "bin.git": Pass, "bin.node": Pass, "bin.catherd": Pass, "docker.engine": Pass, "fs.data_dir": Pass, "fs.artifacts": Pass, "fs.catherd_home": Pass, "fs.workspaces": Pass, "env.proxy": Pass, "launchd.start_at_login": Skipped, "keychain.access": Warn} {
		if got[id].Status != want {
			t.Errorf("%s: %s (%s)", id, got[id].Status, got[id].Detail)
		}
	}
	if !strings.Contains(got["docker.engine"].Detail, "OrbStack") || strings.Contains(got["env.proxy"].Detail, "pw") || !strings.Contains(got["env.proxy"].Detail, "http://proxy.corp:3128") {
		t.Fatal(got["docker.engine"].Detail, got["env.proxy"].Detail)
	}
	if got["keychain.access"].FixCode != "credential_store_memory" || got["keychain.access"].Required {
		t.Fatal(got["keychain.access"])
	}
	for _, c := range got {
		if c.Blocks == nil || c.FixCommand == nil {
			t.Fatal("nil slices must be empty lists", c.ID)
		}
	}
}
func TestRequiredChecksFail(t *testing.T) {
	d, r := happy()
	d.LookPath = lookup("git")
	in := baseInputs()
	in.Mongo = MongoState{OK: false, Err: "not a replica set"}
	in.CatherdBin, in.CatherdVersion = "", ""
	in.CredentialStore = "keychain"
	d.Keychain = func() (string, string) { return Fail, "no keychain" }
	got := byID(Run(context.Background(), d, in))
	for _, id := range []string{"mongo.replica_set", "bin.catherd", "bin.bun", "keychain.access"} {
		if c := got[id]; c.Status != Fail || !c.Required || c.FixCode == "" {
			t.Errorf("%s: %+v", id, c)
		}
	}
	if got["bin.catherd"].FixCode != "catherd_missing" || got["mongo.replica_set"].Blocks[0] != "everything" {
		t.Fatal(got["bin.catherd"], got["mongo.replica_set"])
	}
	if c := got["bin.codex"]; c.Status != Warn || c.Required || c.FixCode != "codex_missing" {
		t.Fatal(c)
	}
	if got["docker.engine"].Status != Warn || got["docker.engine"].FixCode != "docker_missing" {
		t.Fatal(got["docker.engine"])
	}
	for _, call := range r.calls {
		if filepath.Base(call[0]) == "bun" || filepath.Base(call[0]) == "codex" {
			t.Fatal("a missing binary must not be run", call)
		}
	}
	in.CatherdBin, in.CatherdVersion, in.CatherdSupported = "/bin/catherd", "1.6.0", false
	if c := byID(Run(context.Background(), d, in))["bin.catherd"]; c.Status != Fail || c.FixCode != "catherd_version_unsupported" {
		t.Fatal(c)
	}
}
func TestVersionsAndLogins(t *testing.T) {
	d, r := happy()
	r.out["bun --version"] = "1.3.9"
	r.out["codex --version"] = "codex-cli 0.157.0"
	r.out["git --version"] = "git version 2.29.1"
	r.out["node --version"] = "v20.1.0"
	got := byID(Run(context.Background(), d, baseInputs()))
	for id, code := range map[string]string{"bin.bun": "bun_too_old", "bin.codex": "codex_too_old", "bin.git": "git_too_old", "bin.node": "node_too_old"} {
		if got[id].FixCode != code || got[id].Status == Pass {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if got["bin.bun"].Status != Fail || got["bin.codex"].Status != Warn {
		t.Fatal("only bun is required")
	}
	d, r = happy()
	r.exit["codex login status"] = 1
	r.out["claude auth status --json"] = `{"loggedIn":false}`
	got = byID(Run(context.Background(), d, baseInputs()))
	if got["bin.codex"].FixCode != "codex_logged_out" || got["bin.codex"].Status != Warn || got["bin.claude"].FixCode != "claude_logged_out" || got["bin.claude"].Status != Warn {
		t.Fatal(got["bin.codex"], got["bin.claude"])
	}
	d, _ = happy()
	d.LookPath = lookup("bun", "git", "node")
	in := baseInputs()
	in.NeedsClaude = true
	if c := byID(Run(context.Background(), d, in))["bin.claude"]; c.Status != Fail || !c.Required {
		t.Fatal("claude is required when a bound profile uses claude-code rungs", c)
	}
}
func TestFilesystemChecks(t *testing.T) {
	d, _ := happy()
	fs := d.FS.(*fakeFS)
	fs.failDir = "/d/artifacts"
	fs.free = 1 << 30
	got := byID(Run(context.Background(), d, baseInputs()))
	if c := got["fs.artifacts"]; c.Status != Fail || c.FixCode != "dir_not_creatable" || !c.Required {
		t.Fatal(c)
	}
	if c := got["fs.workspaces"]; c.Status != Warn || c.FixCode != "disk_low" || c.Required {
		t.Fatal(c)
	}
}
func TestRealFSWritesOnlyItsProbeFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	fs := RealFS{}
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteProbe(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("the probe file was left behind", entries)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Fatal(st.Mode())
	}
	if free, err := fs.FreeBytes(dir); err != nil || free == 0 {
		t.Fatal(free, err)
	}
}
func caPEM(t *testing.T, notAfter time.Time) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"}, NotBefore: notAfter.Add(-48 * time.Hour), NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
func TestNetworkChecks(t *testing.T) {
	d, _ := happy()
	d.Now = time.Now
	d.Net = fakeNet{results: map[string]NetResult{
		"con_ok": {}, "con_dns": {Stage: "dns", Err: "no such host"}, "con_vpn": {Stage: "tcp", Err: "timeout", Private: true}, "con_proxy": {Stage: "proxy", Err: "refused"},
		"con_expired": {}, "con_soon": {},
	}}
	in := baseInputs()
	in.Connections = []ConnTarget{{ID: "con_ok", Name: "ok"}, {ID: "con_dns", Name: "dns"}, {ID: "con_vpn", Name: "vpn"}, {ID: "con_proxy", Name: "px", ProxyURL: "http://p:1"},
		{ID: "con_expired", Name: "old", CustomCAPEM: caPEM(t, time.Now().Add(-time.Hour))}, {ID: "con_soon", Name: "soon", CustomCAPEM: caPEM(t, time.Now().Add(10*24*time.Hour))}}
	got := byID(Run(context.Background(), d, in))
	want := map[string][2]string{"net.con_ok": {Pass, ""}, "net.con_dns": {Fail, "dns_failed"}, "net.con_vpn": {Fail, "vpn_required_maybe"}, "net.con_proxy": {Fail, "proxy_failed"}, "net.con_expired": {Fail, "ca_expired"}, "net.con_soon": {Warn, "ca_expiring"}}
	for id, w := range want {
		if got[id].Status != w[0] || got[id].FixCode != w[1] || got[id].Required || got[id].Blocks[0] != "connection:"+strings.TrimPrefix(id, "net.") {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if !strings.Contains(got["net.con_vpn"].Detail, "may be required") {
		t.Fatal(got["net.con_vpn"].Detail)
	}
}
func TestRealNetworkAgainstALocalServer(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	if r := (RealNetwork{}).Probe(context.Background(), ConnTarget{ID: "x", BaseURL: up.URL + "/gitlab", Timeout: 2 * time.Second}); r.Stage != "" {
		t.Fatal(r)
	}
	if r := (RealNetwork{}).Probe(context.Background(), ConnTarget{ID: "x", BaseURL: "http://127.0.0.1:1", Timeout: 2 * time.Second}); r.Stage != "tcp" {
		t.Fatal(r)
	}
	if r := (RealNetwork{}).Probe(context.Background(), ConnTarget{ID: "x", BaseURL: up.URL, ProxyURL: "http://127.0.0.1:1", Timeout: 2 * time.Second}); r.Stage != "proxy" {
		t.Fatal(r)
	}
	tlsUp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsUp.Close()
	if r := (RealNetwork{}).Probe(context.Background(), ConnTarget{ID: "x", BaseURL: tlsUp.URL, Timeout: 2 * time.Second}); r.Stage != "tls" {
		t.Fatal("an unknown CA must fail the TLS stage", r)
	}
	if r := (RealNetwork{}).Probe(context.Background(), ConnTarget{ID: "x", BaseURL: "https://nonexistent.invalid", Timeout: 2 * time.Second}); r.Stage != "dns" {
		t.Fatal(r)
	}
}
func TestLaunchd(t *testing.T) {
	d, r := happy()
	in := baseInputs()
	in.LaunchdPlist, in.UID = "/home/u/Library/LaunchAgents/dev.orqen.server.plist", 501
	if c := byID(Run(context.Background(), d, in))["launchd.start_at_login"]; c.Status != Skipped {
		t.Fatal(c)
	}
	in.LaunchdOptIn = true
	if c := byID(Run(context.Background(), d, in))["launchd.start_at_login"]; c.Status != Warn || c.FixCode != "launchd_not_installed" {
		t.Fatal(c)
	}
	d.FS.(*fakeFS).existing[in.LaunchdPlist] = true
	r.out["launchctl print gui/501/dev.orqen.server"] = "state = running"
	if c := byID(Run(context.Background(), d, in))["launchd.start_at_login"]; c.Status != Pass {
		t.Fatal(c)
	}
	plan := BuildLaunchdPlan("/Applications/Orqen & Co/orqen", "/data/Orqen", "/a:/b")
	if !strings.Contains(plan.PlistXML, "<string>serve</string>") || !strings.Contains(plan.PlistXML, "Orqen &amp; Co") || !strings.Contains(plan.PlistXML, "ORQEN_CATHERD_PATH") || plan.PlistPath != "~/Library/LaunchAgents/dev.orqen.server.plist" {
		t.Fatal(plan.PlistXML)
	}
	if !strings.Contains(strings.Join(plan.InstallCommands, "\n"), "launchctl bootstrap gui/$(id -u)") || !strings.Contains(strings.Join(plan.UninstallCommands, "\n"), "bootout") {
		t.Fatal(plan)
	}
}

// Nothing Orqen runs may install, load, log in, or change settings. `codex login status` only reads state.
func TestExecutedCommandsAreDetectOnly(t *testing.T) {
	forbidden := map[string]bool{"install": true, "add": true, "bootstrap": true, "load": true, "login": true, "set": true, "upgrade": true, "bootout": true, "unload": true, "sudo": true, "defaults": true}
	for id, vectors := range Executed {
		for _, argv := range vectors {
			if argv[0] == "sudo" || argv[0] == "defaults" {
				t.Errorf("%s runs %s", id, argv[0])
			}
			for i, a := range argv[1:] {
				if forbidden[a] && !(a == "login" && i+2 < len(argv) && argv[i+2] == "status") {
					t.Errorf("%s: forbidden verb %q in %v", id, a, argv)
				}
			}
		}
	}
	if len(Executed) < 6 {
		t.Fatal("table shrank")
	}
	// The code itself must not build such a vector either.
	d, r := happy()
	in := baseInputs()
	in.LaunchdOptIn, in.LaunchdPlist = true, "/x"
	d.FS.(*fakeFS).existing["/x"] = true
	Run(context.Background(), d, in)
	for _, call := range r.calls {
		for _, a := range call[1:] {
			if forbidden[a] && !(a == "login" && call[len(call)-1] == "status") {
				t.Errorf("ran forbidden verb in %v", call)
			}
		}
	}
}
func TestRunnerCapsOutputAndTimes(t *testing.T) {
	out, exit, err := (ExecRunner{Dir: t.TempDir()}).Run(context.Background(), []string{"/bin/sh", "-c", "yes | head -c 200000"}, []string{"PATH=/bin:/usr/bin"})
	if err != nil || exit != 0 || len(out) != outputCap {
		t.Fatal(len(out), exit, err)
	}
	if _, exit, _ := (ExecRunner{}).Run(context.Background(), []string{"/bin/sh", "-c", "exit 3"}, nil); exit != 3 {
		t.Fatal(exit)
	}
}
