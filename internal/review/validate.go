package review

import "orqen/internal/gitdiff"

// Context is what validation needs to know about the head commit.
type Context struct {
	Diff gitdiff.Diff
	// LineCount returns the number of lines of a file at the head commit, or -1 when the file does not exist there.
	LineCount func(path string) int
}

// Validate marks where each finding can sit: LocationValid when the file exists at head and the line is in range,
// InlineEligible when the new-side line is an added or context line of the diff (or the old-side line a removed or
// context line). A finding with an invalid location is kept but never inline.
func Validate(r *Result, c Context) {
	for i := range r.Findings {
		f := &r.Findings[i]
		f.LocationValid, f.InlineEligible = false, false
		if f.File == "" || f.Line < 1 {
			continue
		}
		if f.Side == "old" {
			f.LocationValid = c.Diff.File(f.File) != nil
		} else if n := c.LineCount(f.File); n >= 0 && f.Line <= n {
			f.LocationValid = true
		}
		f.InlineEligible = f.LocationValid && c.Diff.Eligible(f.File, f.Line, f.Side)
	}
}
