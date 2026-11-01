package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"orqen/internal/catherd"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in: ORQEN_TEST_CATHERD_BIN=$HOME/.bun/bin/catherd go test ./internal/server -run TestCatherdRealSecrets -count=1 -v
// Runs the real `catherd doctor` once, in a temporary organization home and a temporary HOME, with a sentinel as
// TYPESAFE_API_KEY. The sentinel is not a real key; the user's own catherd state is hashed before and after.
func TestCatherdRealSecrets(t *testing.T) {
	bin := os.Getenv("ORQEN_TEST_CATHERD_BIN")
	if bin == "" {
		t.Skip("set ORQEN_TEST_CATHERD_BIN to exercise the real catherd")
	}
	home, _ := os.UserHomeDir()
	guards := map[string]string{}
	for _, d := range []string{filepath.Join(home, ".config", "catherd"), filepath.Join(home, ".local", "share", "catherd"), filepath.Join(home, ".claude", "agents")} {
		guards[d] = dirManifest(d)
	}
	t.Cleanup(func() {
		for d, before := range guards {
			if dirManifest(d) != before {
				t.Errorf("the personal state %s changed", d)
			}
		}
	})
	data := t.TempDir()
	fakeHome := t.TempDir()
	rt := catherd.NewRuntime(t.Context(), catherd.RuntimeConfig{Bin: bin, DataDir: data, UserHome: fakeHome})
	cli := &catherd.CLI{RT: rt, Neutral: data}
	if v, err := cli.Version(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		rt.SetVersion(v)
	}
	// A temporary "personal" home that Orqen must not touch.
	personalData := t.TempDir()
	personal := catherd.NewOrgEnv(personalData, "org_01BX5ZZKBKACTAV9WEVGEMMVRY")
	if err := personal.Ensure(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := cli.Create(ctx, personal, catherd.HostCodex, "mine", nil); err != nil {
		t.Fatal(err)
	}
	personalBefore := dirManifest(personal.Root())

	env := catherd.NewOrgEnv(data, "org_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := env.Ensure(); err != nil {
		t.Fatal(err)
	}
	sentinel := "orqen-SENTINEL-" + newID("r")[2:]
	raw, exit, err := cli.Doctor(ctx, env, catherd.HostCodex, catherd.SecretEnv{"TYPESAFE_API_KEY": sentinel})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("doctor exit %d", exit)
	if strings.Contains(string(raw), sentinel) {
		t.Fatal("the doctor report contains the sentinel")
	}
	var report struct {
		Checks []struct {
			ID, State, Word string
		} `json:"checks"`
	}
	if err = json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	jev := false
	for _, c := range report.Checks {
		jev = jev || c.ID == "jev"
		t.Logf("check %s: %s %s", c.ID, c.State, c.Word)
	}
	if !jev {
		t.Fatalf("the doctor report has no jev row: %s", raw)
	}
	for _, root := range []string{data, fakeHome} {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Name() == "credentials.json" {
				t.Errorf("credentials.json written: %s", p)
			}
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), sentinel) || strings.Contains(p, sentinel) {
				t.Errorf("the sentinel is on disk: %s", p)
			}
			return nil
		})
	}
	if dirManifest(personal.Root()) != personalBefore {
		t.Fatal("the temporary personal home changed")
	}
}
