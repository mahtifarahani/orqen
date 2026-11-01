package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"orqen/internal/catherd"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func dirManifest(dir string) string {
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

// Opt-in (ORQEN_TEST_CATHERD_BIN): the real catherd in temporary homes. The user's own catherd state is hashed before
// and after the whole test, and a temporary "personal" home proves import/validate/publish never write to personal state.
func TestCatherdReal(t *testing.T) {
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
	// A seeded "personal" home that lives outside Orqen's data directory.
	seedData := t.TempDir()
	seedRT := catherd.NewRuntime(t.Context(), catherd.RuntimeConfig{Bin: bin, DataDir: seedData})
	seedCLI := &catherd.CLI{RT: seedRT, Neutral: seedData}
	if v, err := seedCLI.Version(t.Context()); err != nil {
		t.Fatal(err)
	} else {
		seedRT.SetVersion(v)
	}
	seedEnv := catherd.NewOrgEnv(seedData, "org_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	if err := seedEnv.Ensure(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := seedCLI.Create(ctx, seedEnv, catherd.HostCodex, "mine", nil); err != nil {
		t.Fatal(err)
	}
	if out, err := seedCLI.SetKey(ctx, seedEnv, catherd.HostCodex, "mine", "roles", map[string]any{"worker": map[string]any{"access": "read-only"}}, nil); err != nil || !out.Saved {
		t.Fatal(out, err)
	}
	personalBefore := dirManifest(seedEnv.Root())

	f := newTestAppWith(t, true, func(a *App) {
		rt := catherd.NewRuntime(context.Background(), catherd.RuntimeConfig{Bin: bin, DataDir: a.Config.DataDir, PersonalHome: seedEnv.Home()})
		cli := &catherd.CLI{RT: rt, Neutral: a.Config.DataDir}
		if v, err := cli.Version(context.Background()); err == nil {
			rt.SetVersion(v)
		}
		a.SetCatherd(cli, rt)
	})
	f.bootstrap()
	org := f.org("alpha")
	e := &profileEnv{&catherdEnv{f: f, org: org}}
	e.post("/catalog/refresh", map[string]any{"mode": "list"})

	// (e) a full publish into the organization's own home reads back as exactly the draft's document.
	doc := goodProfileDoc()
	id := e.create("main", doc)
	val := e.validate(id)
	if val["status"] != "succeeded" || val["result"].(map[string]any)["valid"] != true {
		lv := f.json("GET", e.base()+"/"+id, nil, nil, 200)["last_validation"]
		t.Fatalf("the real catherd rejected the draft: %v %v", val, lv)
	}
	pub := e.publish(id)
	if pub["status"] != "succeeded" {
		t.Fatal(pub)
	}
	orgEnv := catherd.NewOrgEnv(f.a.Config.DataDir, org)
	raw, err := f.a.Catherd.ShowRaw(ctx, orgEnv, catherd.HostCodex, "o-main-v1")
	if err != nil {
		t.Fatal(err)
	}
	delete(raw, "name")
	if canonical(raw) != canonical(doc) {
		t.Fatalf("read-back differs\n got %s\nwant %s", canonical(raw), canonical(doc))
	}
	rows, _ := f.a.Catherd.ListProfiles(ctx, orgEnv)
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "s-") || r.Active && r.Name != "default" {
			t.Fatal("unexpected profile state:", rows)
		}
	}

	// (f) a document catherd refuses is reported at its path and leaves no staging profile.
	bad := goodProfileDoc()
	bad["roles"].(map[string]any)["worker"].(map[string]any)["enabled"] = false
	badID := e.create("bad", bad)
	e.validate(badID)
	lv := f.json("GET", e.base()+"/"+badID, nil, nil, 200)["last_validation"].(map[string]any)
	found := false
	for _, x := range append(lv["errors"].([]any), lv["orqen_issues"].([]any)...) {
		found = found || x.(map[string]any)["path_display"] == "roles.worker.enabled"
	}
	if lv["valid"] != false || !found {
		t.Fatal(lv)
	}
	rows, _ = f.a.Catherd.ListProfiles(ctx, orgEnv)
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "s-") {
			t.Fatal("a staging profile was left behind:", r.Name)
		}
	}

	// (g) import, validate and publish a personal profile; the personal home is untouched.
	imp := e.op("POST", e.base()+"/import-personal", map[string]any{"personal_name": "mine", "slug": "imp", "name": "Imported"}, nil, 202)
	if imp["status"] != "succeeded" {
		t.Fatal(imp)
	}
	impID := imp["result"].(map[string]any)["profile_id"].(string)
	d := f.json("GET", e.base()+"/"+impID, nil, nil, 200)["draft"].(map[string]any)["doc"].(map[string]any)
	if d["roles"].(map[string]any)["worker"].(map[string]any)["access"] != "read-only" {
		t.Fatal(d)
	}
	e.validate(impID)
	if dirManifest(seedEnv.Root()) != personalBefore {
		t.Fatal("the personal home changed")
	}
}
