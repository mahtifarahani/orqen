// Package gitdiff parses unified diffs from `git diff`. It is pure.
package gitdiff

import (
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Line is one line of a hunk. Kind is ' ', '+' or '-'. Old and New are 1-based line numbers (0 when absent).
type Line struct {
	Kind     byte
	Old, New int
	Text     string
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart, OldLines, NewStart, NewLines int
	Lines                                  []Line
}

// File is one changed file.
type File struct {
	Path      string
	OldPath   string
	Status    string // added, modified, deleted, renamed
	Binary    bool
	Additions int
	Deletions int
	Hunks     []Hunk
}

// Diff is a whole diff.
type Diff struct{ Files []File }

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func unquote(p string) string {
	if strings.HasPrefix(p, `"`) {
		if u, err := strconv.Unquote(p); err == nil {
			return u
		}
	}
	return p
}

// Parse reads the output of `git diff` (default prefixes, no color, no external diff).
func Parse(text string) (Diff, error) {
	var d Diff
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var cur *File
	var hunk *Hunk
	oldN, newN := 0, 0
	flush := func() {
		if hunk != nil && cur != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	finish := func() {
		flush()
		if cur != nil {
			d.Files = append(d.Files, *cur)
			cur = nil
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			finish()
			rest := strings.TrimPrefix(line, "diff --git ")
			cur = &File{Status: "modified"}
			// "a/x b/x": the two halves are equal for everything but renames, which the later lines fix.
			if i := strings.Index(rest, " b/"); i > 0 {
				cur.OldPath = unquote(strings.TrimPrefix(rest[:i], "a/"))
				cur.Path = unquote(strings.TrimPrefix(rest[i+1:], "b/"))
			}
		case cur == nil:
			continue
		case hunk == nil && strings.HasPrefix(line, "new file mode"):
			cur.Status = "added"
		case hunk == nil && strings.HasPrefix(line, "deleted file mode"):
			cur.Status = "deleted"
		case hunk == nil && strings.HasPrefix(line, "rename from "):
			cur.Status, cur.OldPath = "renamed", unquote(strings.TrimPrefix(line, "rename from "))
		case hunk == nil && strings.HasPrefix(line, "rename to "):
			cur.Path = unquote(strings.TrimPrefix(line, "rename to "))
		case hunk == nil && strings.HasPrefix(line, "Binary files "), hunk == nil && strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case hunk == nil && strings.HasPrefix(line, "--- "):
			if p := unquote(strings.TrimPrefix(line, "--- ")); p != "/dev/null" {
				cur.OldPath = strings.TrimPrefix(p, "a/")
			}
		case hunk == nil && strings.HasPrefix(line, "+++ "):
			if p := unquote(strings.TrimPrefix(line, "+++ ")); p != "/dev/null" {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@"):
			flush()
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return Diff{}, fmt.Errorf("gitdiff: bad hunk header %q", line)
			}
			hunk = &Hunk{OldStart: atoi(m[1], 0), OldLines: atoi(m[2], 1), NewStart: atoi(m[3], 0), NewLines: atoi(m[4], 1)}
			oldN, newN = hunk.OldStart, hunk.NewStart
		case hunk != nil:
			if line == `\ No newline at end of file` {
				continue
			}
			if line == "" {
				line = " "
			}
			switch line[0] {
			case '+':
				hunk.Lines = append(hunk.Lines, Line{Kind: '+', New: newN, Text: line[1:]})
				newN++
				cur.Additions++
			case '-':
				hunk.Lines = append(hunk.Lines, Line{Kind: '-', Old: oldN, Text: line[1:]})
				oldN++
				cur.Deletions++
			case ' ':
				hunk.Lines = append(hunk.Lines, Line{Kind: ' ', Old: oldN, New: newN, Text: line[1:]})
				oldN++
				newN++
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Diff{}, err
	}
	finish()
	for i := range d.Files {
		if d.Files[i].Path == "" {
			d.Files[i].Path = d.Files[i].OldPath
		}
	}
	return d, nil
}

// File returns the changed file with that (new) path.
func (d Diff) File(path string) *File {
	for i := range d.Files {
		if d.Files[i].Path == path {
			return &d.Files[i]
		}
	}
	return nil
}
