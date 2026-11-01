package knowledge

import (
	"reflect"
	"strings"
)

const maxDiffLines = 2000

type Hunk struct {
	Op    string   `json:"op"` // equal | add | del
	Lines []string `json:"lines"`
}

type TextPair struct {
	A string `json:"a"`
	B string `json:"b"`
}

type ClaimsDiff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

type DiffResult struct {
	Title    TextPair   `json:"title"`
	Hunks    []Hunk     `json:"body_hunks"`
	TooLarge bool       `json:"too_large"`
	Claims   ClaimsDiff `json:"claims"`
	Validity TextPair   `json:"validity"`
}

func validityText(v Version) string {
	f := func(s string, t interface{ Format(string) string }, nilp bool) string {
		if nilp {
			return s + ":-"
		}
		return s + ":" + t.Format("2006-01-02")
	}
	a, b := "from:-", "until:-"
	if v.ValidFrom != nil {
		a = f("from", v.ValidFrom, false)
	}
	if v.ValidUntil != nil {
		b = f("until", v.ValidUntil, false)
	}
	return a + " " + b
}

// Diff compares two versions: a line LCS of the bodies (up to 2000 lines per side, otherwise TooLarge), the claims by
// id and the validity window.
func Diff(a, b Version) DiffResult {
	d := DiffResult{Title: TextPair{a.Title, b.Title}, Hunks: []Hunk{},
		Claims:   ClaimsDiff{Added: []string{}, Removed: []string{}, Changed: []string{}},
		Validity: TextPair{validityText(a), validityText(b)}}
	al, bl := splitLines(a.Body), splitLines(b.Body)
	if len(al) > maxDiffLines || len(bl) > maxDiffLines {
		d.TooLarge = true
	} else {
		d.Hunks = lcsHunks(al, bl)
	}
	am, bm := map[string]Claim{}, map[string]Claim{}
	for _, c := range a.Claims {
		am[c.ID] = c
	}
	for _, c := range b.Claims {
		bm[c.ID] = c
	}
	for _, c := range b.Claims {
		old, ok := am[c.ID]
		switch {
		case !ok:
			d.Claims.Added = append(d.Claims.Added, c.ID)
		case !sameClaim(old, c):
			d.Claims.Changed = append(d.Claims.Changed, c.ID)
		}
	}
	for _, c := range a.Claims {
		if _, ok := bm[c.ID]; !ok {
			d.Claims.Removed = append(d.Claims.Removed, c.ID)
		}
	}
	return d
}

func sameClaim(a, b Claim) bool {
	return a.Text == b.Text && a.Critical == b.Critical && reflect.DeepEqual(a.Fact, b.Fact) &&
		reflect.DeepEqual(a.Template, b.Template) && reflect.DeepEqual(nz(a.Evidence), nz(b.Evidence))
}
func nz(e []Evidence) []Evidence {
	if e == nil {
		return []Evidence{}
	}
	return e
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}

func lcsHunks(a, b []string) []Hunk {
	n, m := len(a), len(b)
	t := make([][]int, n+1)
	for i := range t {
		t[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	var hs []Hunk
	push := func(op, line string) {
		if len(hs) > 0 && hs[len(hs)-1].Op == op {
			hs[len(hs)-1].Lines = append(hs[len(hs)-1].Lines, line)
			return
		}
		hs = append(hs, Hunk{Op: op, Lines: []string{line}})
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			push("equal", a[i])
			i++
			j++
		case t[i+1][j] >= t[i][j+1]:
			push("del", a[i])
			i++
		default:
			push("add", b[j])
			j++
		}
	}
	for ; i < n; i++ {
		push("del", a[i])
	}
	for ; j < m; j++ {
		push("add", b[j])
	}
	if hs == nil {
		hs = []Hunk{}
	}
	return hs
}
