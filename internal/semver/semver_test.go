package semver

import "testing"

func TestFindAndCompare(t *testing.T) {
	for in, want := range map[string]Version{"1.5.0": {1, 5, 0}, "v2.1.282 (Claude Code)": {2, 1, 282}, "codex-cli 0.159.2": {0, 159, 2}, "git version 2.39": {2, 39, 0}, "bun 1.4.2\n": {1, 4, 2}} {
		got, ok := Find(in)
		if !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := Find("no version"); ok {
		t.Fatal("matched")
	}
	a, b := Version{1, 5, 0}, Version{1, 6, 0}
	if a.Compare(b) != -1 || b.Compare(a) != 1 || a.Compare(a) != 0 || !a.InWindow(Version{1, 5, 0}, b) || b.InWindow(Version{1, 5, 0}, b) || !(Version{1, 5, 9}).InWindow(a, b) {
		t.Fatal("compare")
	}
	if a.String() != "1.5.0" || !b.AtLeast(a) || a.AtLeast(b) {
		t.Fatal("string")
	}
}
