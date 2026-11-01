package gitdiff

// Eligible reports whether a review comment can sit on (path, line) as an inline discussion: on the new side the
// line must be an added or context line of a hunk; on the old side a removed or context line.
func (d Diff) Eligible(path string, line int, side string) bool {
	f := d.File(path)
	if f == nil || f.Binary || line < 1 {
		return false
	}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if side == "old" {
				if l.Old == line && (l.Kind == '-' || l.Kind == ' ') {
					return true
				}
			} else if l.New == line && (l.Kind == '+' || l.Kind == ' ') {
				return true
			}
		}
	}
	return false
}

// Paths lists the changed paths in order.
func (d Diff) Paths() []string {
	out := make([]string, 0, len(d.Files))
	for _, f := range d.Files {
		out = append(out, f.Path)
	}
	return out
}

// Totals sums additions and deletions.
func (d Diff) Totals() (add, del int) {
	for _, f := range d.Files {
		add += f.Additions
		del += f.Deletions
	}
	return
}
