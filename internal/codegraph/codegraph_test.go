package codegraph

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"orqen/internal/extensions"
)

// TestMain doubles as the fake GitNexus: the wrapper script written by wrapper() runs this test binary with
// __fake_gitnexus__ <scenario file> <args...>. No real tool is ever started.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "__fake_gitnexus__" {
		os.Exit(RunFakeCLI(os.Args[3:], os.Stdout, os.Args[2]))
	}
	os.Exit(m.Run())
}

type env struct {
	org     string
	wrapper string
	scn     string
	permit  extensions.Permit
	cli     *CLI
}

func newEnv(t *testing.T) env {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	org := filepath.Join(root, "codegraph", "org_1")
	if err := os.MkdirAll(org, 0o700); err != nil {
		t.Fatal(err)
	}
	scn := filepath.Join(root, "scenario.json")
	exe, _ := os.Executable()
	wrapper := filepath.Join(root, "gitnexus")
	script := fmt.Sprintf("#!/bin/sh\nexec %q __fake_gitnexus__ %q \"$@\"\n", exe, scn)
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g, _ := extensions.Get("gitnexus")
	p, reason := extensions.Usable(g, extensions.Snapshot{Status: extensions.StatusEnabled, Path: wrapper, Version: "fake", SHA256: "x",
		License: &extensions.Acceptance{LicenseID: g.License.SPDX}, Credentials: map[string]bool{}})
	if reason != "" {
		t.Fatal(reason)
	}
	cli := NewCLI(p, org)
	cli.Mode = extensions.SandboxBestEffort
	return env{org: org, wrapper: wrapper, scn: scn, permit: p, cli: cli}
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

var fixture = map[string]string{
	"a.go":     "package a\n// imports: b.go\n// symbol: Alpha\n",
	"b.go":     "package b\n// imports: c.go\n// symbol: Beta\n",
	"c.go":     "package c\n// symbol: Gamma\n",
	"sub/d.go": "package d\n// imports: a.go\n",
}

func TestFakeCLIRoundTripAndSchemas(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tree := filepath.Join(e.org, "trees", "r", "abc")
	writeTree(t, tree, fixture)
	idx := filepath.Join(e.org, "indexes", "r", "abc")

	h, err := e.cli.Version(ctx)
	if err != nil || h.Schema != "v1" || h.Version != "fake-1.0" {
		t.Fatalf("version: %+v %v", h, err)
	}
	an, err := e.cli.Analyze(ctx, tree, idx)
	if err != nil || an.Files != 4 || an.Symbols != 3 {
		t.Fatalf("analyze: %+v %v", an, err)
	}
	imp, err := e.cli.Impact(ctx, idx, "b.go", KindFile, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range imp.Items {
		got = append(got, fmt.Sprintf("%s:%s:%d", it.Relation, it.Path, it.Depth))
	}
	if strings.Join(got, ",") != "imported_by:a.go:1,imports:c.go:1,imported_by:sub/d.go:2" {
		t.Fatalf("impact: %v", got)
	}
	// a symbol target resolves to its file
	if sym, err := e.cli.Impact(ctx, idx, "Gamma", KindSymbol, 1, 10); err != nil || len(sym.Items) != 1 || sym.Items[0].Path != "b.go" {
		t.Fatalf("symbol impact: %+v %v", sym, err)
	}
	if lim, _ := e.cli.Impact(ctx, idx, "b.go", KindFile, 2, 1); !lim.Truncated || len(lim.Items) != 1 {
		t.Fatalf("limit: %+v", lim)
	}
	cx, err := e.cli.Context(ctx, idx, "a.go", 10)
	if err != nil || len(cx.Imports) != 1 || len(cx.ImportedBy) != 1 || cx.Symbols[0] != "Alpha" {
		t.Fatalf("context: %+v %v", cx, err)
	}
	// the in-process Fake gives the same answers
	f := &Fake{}
	idx2 := filepath.Join(e.org, "indexes", "r", "fake")
	if _, err = f.Analyze(ctx, tree, idx2); err != nil {
		t.Fatal(err)
	}
	if imp2, _ := f.Impact(ctx, idx2, "b.go", KindFile, 2, 10); fmt.Sprint(imp2.Items) != fmt.Sprint(imp.Items) {
		t.Fatalf("fake differs from the cli: %v vs %v", imp2.Items, imp.Items)
	}
	// every fake output validates against its schema
	for name, v := range map[string]any{"version": h, "analyze": an, "impact": imp, "context": cx} {
		raw, _ := json.Marshal(v)
		sb, _ := SchemaFile(name)
		if err := extensions.ValidateJSON(sb, raw); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range SchemaNames {
		if _, err := SchemaFile(name); err != nil {
			t.Error(err)
		}
	}
}

func TestSchemaViolationIsInvalidOutputAndFailureKinds(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tree := filepath.Join(e.org, "trees", "r", "abc")
	writeTree(t, tree, fixture)
	idx := filepath.Join(e.org, "indexes", "r", "abc")
	set := func(s string) { _ = os.WriteFile(e.scn, []byte(s), 0o600) }

	set(`{"gitnexus":{"wrong_schema":true}}`)
	if _, err := e.cli.Analyze(ctx, tree, idx); !IsKind(err, KindInvalidOutput) {
		t.Fatalf("wrong schema: %v", err)
	}
	set(`{"gitnexus":{"fail_verb":"analyze"}}`)
	if _, err := e.cli.Analyze(ctx, tree, idx); !IsKind(err, KindFailed) {
		t.Fatalf("fail_verb: %v", err)
	}
	set(`{"gitnexus":{"slow_ms":3000}}`)
	e.cli.QueryTimeout = 300 * time.Millisecond
	if _, err := e.cli.Version(ctx); !IsKind(err, KindTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	set(`{"gitnexus":{"version":"9.9.9"}}`)
	e.cli.QueryTimeout = 20 * time.Second
	if h, err := e.cli.Version(ctx); err != nil || h.Version != "9.9.9" {
		t.Fatalf("scenario version: %+v %v", h, err)
	}
	// output over the cap
	e.cli.MaxOut = 5
	if _, err := e.cli.Version(ctx); !IsKind(err, KindInvalidOutput) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestPathsAndTargetsAreConfined(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	outside := t.TempDir()
	writeTree(t, outside, fixture)
	if _, err := e.cli.Analyze(ctx, outside, filepath.Join(e.org, "idx")); !IsKind(err, KindBadPath) {
		t.Fatalf("tree outside the org dir: %v", err)
	}
	tree := filepath.Join(e.org, "trees", "t")
	writeTree(t, tree, fixture)
	if _, err := e.cli.Analyze(ctx, tree, filepath.Join(outside, "idx")); !IsKind(err, KindBadPath) {
		t.Fatalf("out dir outside the org dir: %v", err)
	}
	// a symlink inside the org dir that points out is outside too
	link := filepath.Join(e.org, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cli.Analyze(ctx, tree, filepath.Join(link, "idx")); !IsKind(err, KindBadPath) {
		t.Fatalf("out dir through a symlink: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "idx")); err == nil {
		t.Fatal("something was written outside")
	}
	idx := filepath.Join(e.org, "idx")
	for _, tc := range []struct{ kind, target string }{{KindFile, "../etc/passwd"}, {KindFile, "/etc/passwd"}, {KindFile, "--output"}, {KindSymbol, "a b"}, {KindSymbol, "-x"}, {"other", "a"}} {
		if _, err := e.cli.Impact(ctx, idx, tc.target, tc.kind, 1, 5); !IsKind(err, KindBadPath) {
			t.Errorf("%v must be refused: %v", tc, err)
		}
	}
	if _, err := e.cli.Impact(ctx, idx, "a.go", KindFile, 3, 5); !IsKind(err, KindBadPath) {
		t.Error("depth 3")
	}
}

func TestEnvIsExplicitAndInsideTheOrgDir(t *testing.T) {
	e := newEnv(t)
	env := e.cli.env()
	want := map[string]bool{"HOME": true, "TMPDIR": true, "PATH": true, "LANG": true, "NO_COLOR": true, "GITNEXUS_NO_TELEMETRY": true}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if !want[k] {
			t.Errorf("unexpected variable %s", k)
		}
		delete(want, k)
		if (k == "HOME" || k == "TMPDIR") && !strings.HasPrefix(v, e.org+string(filepath.Separator)) {
			t.Errorf("%s=%s is outside the org dir", k, v)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing variables: %v", want)
	}
}

// TestArgvAllowlist: cli.go starts only the four verb vectors; the global verbs never appear as string literals.
func TestArgvAllowlist(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "cli.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"setup": true, "init": true, "install": true, "serve": true, "mcp": true, "update": true, "login": true}
	ast.Inspect(f, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
			v := strings.Trim(l.Value, "\"`")
			if banned[v] {
				t.Errorf("cli.go contains the literal %q", v)
			}
		}
		return true
	})
	raw, _ := os.ReadFile("cli.go")
	for _, want := range []string{`"--version"`, `"analyze", "--index-only"`, `"query", "impact"`, `"query", "context"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing vector %s", want)
		}
	}
}

func tarOf(t *testing.T, entries ...tar.Header) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "x"
			h.Size = 1
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			_, _ = tw.Write([]byte(body))
		}
	}
	_ = tw.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestExtractTarRefusesEscapes(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "tree")
	ok := tarOf(t,
		tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "abc"}},
		tar.Header{Name: "src/", Typeflag: tar.TypeDir, Mode: 0o755},
		tar.Header{Name: "src/a.go", Typeflag: tar.TypeReg, Mode: 0o644},
		tar.Header{Name: "run.sh", Typeflag: tar.TypeReg, Mode: 0o755})
	if err := ExtractTar(ok, dest); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "src", "a.go")); err != nil || string(b) != "x" {
		t.Fatalf("file: %q %v", b, err)
	}
	for name, h := range map[string]tar.Header{
		"dotdot":    {Name: "../x", Typeflag: tar.TypeReg},
		"nested":    {Name: "a/../../x", Typeflag: tar.TypeReg},
		"absolute":  {Name: "/tmp/x", Typeflag: tar.TypeReg},
		"symlink":   {Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"hardlink":  {Name: "h", Typeflag: tar.TypeLink, Linkname: "src/a.go"},
		"device":    {Name: "dev", Typeflag: tar.TypeChar},
		"backslash": {Name: `a\..\x`, Typeflag: tar.TypeReg},
	} {
		d := filepath.Join(t.TempDir(), "tree")
		if err := ExtractTar(tarOf(t, h), d); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	// a file cannot be written through a directory that is a symlink planted earlier in the same archive
	d := filepath.Join(t.TempDir(), "tree")
	if err := ExtractTar(tarOf(t, tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/tmp"}, tar.Header{Name: "l/x", Typeflag: tar.TypeReg}), d); err == nil {
		t.Error("symlink then file through it must be refused")
	}
	if err := ExtractTar(ok, "relative"); err == nil {
		t.Error("relative destination")
	}
}

func TestImpactItemHeaderAndBudget(t *testing.T) {
	var items []Impacted
	for i := 0; i < 50; i++ {
		items = append(items, Impacted{Path: fmt.Sprintf("pkg/file_%02d.go", i), Relation: RelImportedBy, Depth: 1})
	}
	it := ImpactItem(ImpactResult{Target: "a.go", Items: items}, "0123456789abcdef0123", 400, "org_1")
	if !strings.HasPrefix(it.Text, "code impact commit=0123456789ab target=a.go") {
		t.Fatalf("header: %q", it.Text)
	}
	if len(it.Text) > 400 || !strings.HasSuffix(it.Text, "[truncated]") {
		t.Fatalf("budget and note: %d %q", len(it.Text), it.Text[len(it.Text)-30:])
	}
	if it.Kind != "code_context" || it.SourceID != "code_impact" || !it.External || it.Trust != "external" || it.Critical {
		t.Fatalf("item flags: %+v", it)
	}
	small := ImpactItem(ImpactResult{Target: "a\nb", Items: items[:1]}, "abc", 2048, "o")
	if strings.Contains(small.Text, "[truncated]") || strings.Contains(small.Text, "a\nb") {
		t.Fatalf("small item: %q", small.Text)
	}
}

func TestSandboxEnforcedWorksOrIsReportedUnavailable(t *testing.T) {
	e := newEnv(t)
	e.cli.Mode = extensions.SandboxEnforce
	h, err := e.cli.Version(context.Background())
	if extensions.SandboxAvailable() {
		if err != nil || h.Schema != "v1" {
			t.Fatalf("under the sandbox: %+v %v", h, err)
		}
		return
	}
	if !IsKind(err, KindSandbox) {
		t.Fatalf("no sandbox in enforce mode must be unusable: %v", err)
	}
}
