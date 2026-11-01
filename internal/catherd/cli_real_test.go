package catherd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// manifest hashes every file under dir (relative path, type and content) so a before/after comparison catches any write.
func manifest(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		info, _ := os.Lstat(p)
		line := rel + " " + info.Mode().Type().String()
		if info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			s := sha256.Sum256(b)
			line += " " + hex.EncodeToString(s[:])
		}
		lines = append(lines, line)
		return nil
	})
	sort.Strings(lines)
	s := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(s[:])
}

// realCLI skips unless ORQEN_TEST_CATHERD_BIN names a catherd executable. Every call uses a temporary organization
// namespace; the user's own catherd state is hashed before and after to prove it was not touched.
func realCLI(t *testing.T) (*CLI, *OrgEnv) {
	t.Helper()
	bin := os.Getenv("ORQEN_TEST_CATHERD_BIN")
	if bin == "" {
		t.Skip("set ORQEN_TEST_CATHERD_BIN to exercise the real catherd")
	}
	home, _ := os.UserHomeDir()
	guards := map[string]string{}
	for _, d := range []string{filepath.Join(home, ".config", "catherd"), filepath.Join(home, ".claude", "agents")} {
		guards[d] = manifest(t, d)
	}
	t.Cleanup(func() {
		for d, before := range guards {
			if after := manifest(t, d); after != before {
				t.Errorf("the personal state %s changed", d)
			}
		}
	})
	data := t.TempDir()
	rt := NewRuntime(context.Background(), RuntimeConfig{Bin: bin, DataDir: data, PersonalHome: filepath.Join(data, "personal-home")})
	cli := &CLI{RT: rt, Neutral: filepath.Join(data, "neutral")}
	if err := os.MkdirAll(cli.Neutral, 0o700); err != nil {
		t.Fatal(err)
	}
	v, err := cli.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rt.SetVersion(v)
	env := NewOrgEnv(data, orgA)
	if err = env.Ensure(); err != nil {
		t.Fatal(err)
	}
	return cli, env
}
func TestRealVersionListAndCatalog(t *testing.T) {
	cli, env := realCLI(t)
	ctx := context.Background()
	if !cli.RT.Supported() {
		t.Fatalf("catherd %s is outside the supported window", cli.RT.Version())
	}
	before := manifest(t, env.Root())
	rows, err := cli.ListProfiles(ctx, env)
	if err != nil || len(rows) != 1 || rows[0].Name != "default" {
		t.Fatal(rows, err)
	}
	raw, err := cli.ShowRaw(ctx, env, HostCodex, "default")
	if err != nil || raw["schema"] == nil {
		t.Fatal(raw, err)
	}
	if after := manifest(t, env.Root()); after != before {
		t.Fatal("read commands created files in a fresh organization home")
	}
	b, err := cli.CatalogList(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ParseCatalog(b)
	if err != nil || len(c.Models) == 0 || c.Models[0].ID == "" {
		t.Fatal(len(c.Models), err)
	}
	// An unknown profile is a catherd error with a code, not a crash.
	if _, err = cli.ShowRaw(ctx, env, HostCodex, "nope"); err == nil {
		t.Fatal("expected an error")
	}
}
func TestRealStagingProfileRoundTrip(t *testing.T) {
	cli, env := realCLI(t)
	ctx := context.Background()
	if err := cli.Create(ctx, env, HostCodex, "s-probe", nil); err != nil {
		t.Fatal(err)
	}
	out, err := cli.SetKey(ctx, env, HostCodex, "s-probe", "roles", map[string]any{"worker": map[string]any{"enabled": false}}, nil)
	if err != nil || out.Saved || len(out.Refused) == 0 || out.Refused[0].PathDisplay != "roles.worker.enabled" {
		t.Fatalf("%+v %v", out, err)
	}
	out, err = cli.SetKey(ctx, env, HostCodex, "s-probe", "budget", map[string]any{"usd": 5}, nil)
	if err != nil || !out.Saved {
		t.Fatal(out, err)
	}
	if _, err = cli.SetKey(ctx, env, HostCodex, "s-probe", "surprise", 1, nil); err == nil || err.(*Error).Code != CodeRejected {
		t.Fatal("strict keys", err)
	}
	v, err := cli.Validate(ctx, env, HostCodex, "s-probe", nil)
	if err != nil || !v.Valid {
		t.Fatal(v, err)
	}
	raw, _ := cli.ShowRaw(ctx, env, HostCodex, "s-probe")
	if raw["budget"].(map[string]any)["usd"].(interface{ String() string }).String() != "5" {
		t.Fatal(raw["budget"])
	}
	if err = cli.Remove(ctx, env, "s-probe"); err != nil {
		t.Fatal(err)
	}
	rows, _ := cli.ListProfiles(ctx, env)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
}
