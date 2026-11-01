package gitdiff

import "testing"

const sample = `diff --git a/src/a.go b/src/a.go
index 111..222 100644
--- a/src/a.go
+++ b/src/a.go
@@ -1,4 +1,5 @@
 package a
 
-func Old() {}
+func New() {}
+func Extra() {}
 
@@ -20,2 +21,2 @@ func tail
 keep
-gone
+kept
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+one
+two
diff --git a/old_test.go b/old_test.go
deleted file mode 100644
--- a/old_test.go
+++ /dev/null
@@ -1,2 +0,0 @@
-func TestX(t *testing.T) {}
-
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
diff --git a/r1.txt b/r2.txt
similarity index 100%
rename from r1.txt
rename to r2.txt
`

func TestParse(t *testing.T) {
	d, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 5 {
		t.Fatalf("%d files: %+v", len(d.Files), d.Paths())
	}
	a := d.File("src/a.go")
	if a == nil || a.Additions != 3 || a.Deletions != 2 || len(a.Hunks) != 2 || a.Status != "modified" {
		t.Fatalf("%+v", a)
	}
	if n := d.File("new.txt"); n == nil || n.Status != "added" || n.Additions != 2 {
		t.Fatalf("%+v", n)
	}
	if o := d.File("old_test.go"); o == nil || o.Status != "deleted" || o.Deletions != 2 {
		t.Fatalf("%+v", o)
	}
	if b := d.File("img.png"); b == nil || !b.Binary {
		t.Fatal("binary")
	}
	if r := d.File("r2.txt"); r == nil || r.Status != "renamed" || r.OldPath != "r1.txt" {
		t.Fatalf("%+v", r)
	}
	add, del := d.Totals()
	if add != 5 || del != 4 {
		t.Fatal(add, del)
	}
}

func TestEligibleLines(t *testing.T) {
	d, _ := Parse(sample)
	cases := []struct {
		path string
		line int
		side string
		want bool
	}{
		{"src/a.go", 1, "new", true},  // context
		{"src/a.go", 3, "new", true},  // added
		{"src/a.go", 4, "new", true},  // added
		{"src/a.go", 5, "new", true},  // context
		{"src/a.go", 6, "new", false}, // outside any hunk
		{"src/a.go", 3, "old", true},  // the removed Old()
		{"src/a.go", 3, "old", true},  // the removed Old()
		{"src/a.go", 21, "new", true}, // context in the second hunk
		{"src/a.go", 22, "new", true}, // added
		{"new.txt", 2, "new", true},
		{"new.txt", 3, "new", false},
		{"old_test.go", 1, "old", true},
		{"old_test.go", 1, "new", false},
		{"img.png", 1, "new", false},
		{"missing.go", 1, "new", false},
		{"src/a.go", 0, "new", false},
	}
	for _, c := range cases {
		if got := d.Eligible(c.path, c.line, c.side); got != c.want {
			t.Errorf("%s:%d %s = %v, want %v", c.path, c.line, c.side, got, c.want)
		}
	}
}
