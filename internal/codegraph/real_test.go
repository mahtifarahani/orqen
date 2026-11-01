package codegraph

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"orqen/internal/extensions"
)

// TestGitNexusReal runs the assumed contract v1 against a real GitNexus. It is gated and never runs by default:
//
//	ORQEN_TEST_GITNEXUS_BIN=<abs path> ORQEN_TEST_GITNEXUS_LICENSE_OK=1 go test ./internal/codegraph -run TestGitNexusReal -v
//
// ORQEN_TEST_GITNEXUS_LICENSE_OK=1 is the user's own statement that their use of GitNexus is licensed. The test
// indexes a 3-file fixture tree in a temp dir with the sandbox enforced and prints a structured diff on mismatch.
func TestGitNexusReal(t *testing.T) {
	bin := os.Getenv("ORQEN_TEST_GITNEXUS_BIN")
	if bin == "" || os.Getenv("ORQEN_TEST_GITNEXUS_LICENSE_OK") != "1" {
		t.Skip("set ORQEN_TEST_GITNEXUS_BIN and ORQEN_TEST_GITNEXUS_LICENSE_OK=1 to run against a real GitNexus")
	}
	g, _ := extensions.Get("gitnexus")
	p, reason := extensions.Usable(g, extensions.Snapshot{Status: extensions.StatusEnabled, Path: bin, Version: "real", SHA256: "real",
		License: &extensions.Acceptance{LicenseID: g.License.SPDX}, Credentials: map[string]bool{}})
	if reason != "" {
		t.Fatal(reason)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	org := filepath.Join(root, "codegraph", "org_real")
	tree := filepath.Join(org, "trees", "fixture")
	writeTree(t, tree, fixture)
	cli := NewCLI(p, org)
	ctx := context.Background()
	if h, err := cli.Version(ctx); err != nil {
		t.Errorf("schema diff (version): %v", err)
	} else {
		t.Logf("version: %+v", h)
	}
	idx := filepath.Join(org, "indexes", "fixture")
	if _, err := cli.Analyze(ctx, tree, idx); err != nil {
		t.Fatalf("schema diff (analyze): %v", err)
	}
	if _, err := cli.Impact(ctx, idx, "b.go", KindFile, 2, 10); err != nil {
		t.Errorf("schema diff (impact): %v", err)
	}
	if _, err := cli.Context(ctx, idx, "a.go", 10); err != nil {
		t.Errorf("schema diff (context): %v", err)
	}
}
