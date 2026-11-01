package execution

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Lane is one lane of the architect's plan, read from the run folder.
type Lane struct {
	ID    string
	Title string
	Owns  []string
	After []string
}

// Plan is plan.md and its lanes.
type Plan struct {
	Text  string
	Lanes []Lane // in After: order
}

// PlanError carries a stable reason code.
type PlanError struct{ Code, Detail string }

func (e *PlanError) Error() string { return "plan invalid: " + e.Code + ": " + e.Detail }

const (
	maxPlanFile = 256 << 10
	maxLanes    = 3
)

var laneIDPattern = regexp.MustCompile(`^M(\d+)\.L(\d+)$`)

// ValidateRunDir resolves a run folder reported by catherd and requires it to lie inside the organization's
// catherd data directory (symlinks resolved).
func ValidateRunDir(dataRoot, dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dataRoot)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", errors.New("the run folder is outside the organization's catherd data directory")
	}
	return real, nil
}

// ReadRunFile reads a file below runDir, refusing escapes, non-regular files and anything over max bytes.
func ReadRunFile(runDir, rel string, max int64) ([]byte, error) {
	clean := filepath.Clean(rel)
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return nil, errors.New("the path leaves the run folder")
	}
	root, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, err
	}
	full, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return nil, errors.New("the path leaves the run folder")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > max {
		return nil, fmt.Errorf("the file is larger than %d bytes", max)
	}
	return io.ReadAll(io.LimitReader(f, max))
}

// ReadReply reads a role's reply file (replyPath is relative to the run folder), at most max bytes.
func ReadReply(runDir, replyPath string, max int64) ([]byte, error) {
	return ReadRunFile(runDir, replyPath, max)
}

func headerValue(text, label string) string {
	re := regexp.MustCompile(`(?im)^\s*[*_]*` + label + `[*_]*\s*:[*_]*\s*(.*)$`)
	// header lines sit under the title until the first blank line or heading
	lines := strings.Split(text, "\n")
	start := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "# ") {
			start = i + 1
			break
		}
	}
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for i := start; i < len(lines) && strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(lines[i], "#"); i++ {
		if m := re.FindStringSubmatch(lines[i]); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.Trim(strings.TrimSpace(p), "`")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParsePlan reads plan.md and lanes/*.md from the validated run folder. It accepts one milestone (M1) with one to
// three lanes whose After: entries name other lanes without a cycle, and returns the lanes in execution order.
func ParsePlan(runDir string) (Plan, error) {
	text, err := ReadRunFile(runDir, "plan.md", maxPlanFile)
	if err != nil {
		return Plan{}, &PlanError{"plan_missing", "plan.md could not be read"}
	}
	entries, err := os.ReadDir(filepath.Join(runDir, "lanes"))
	if err != nil {
		return Plan{}, &PlanError{"no_lanes", "the lanes folder could not be read"}
	}
	var lanes []Lane
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		id := strings.TrimSuffix(name, ".md")
		m := laneIDPattern.FindStringSubmatch(id)
		if m == nil {
			return Plan{}, &PlanError{"bad_lane_id", id}
		}
		if m[1] != "1" {
			return Plan{}, &PlanError{"multiple_milestones", id}
		}
		body, err := ReadRunFile(runDir, filepath.Join("lanes", name), maxPlanFile)
		if err != nil {
			return Plan{}, &PlanError{"lane_unreadable", id}
		}
		title := id
		if m := regexp.MustCompile(`(?m)^#\s+(.+)$`).FindStringSubmatch(string(body)); m != nil {
			title = strings.TrimSpace(m[1])
		}
		lanes = append(lanes, Lane{ID: id, Title: title, Owns: splitList(headerValue(string(body), "owns")), After: splitList(headerValue(string(body), "after"))})
	}
	switch {
	case len(lanes) == 0:
		return Plan{}, &PlanError{"no_lanes", "the plan has no lane"}
	case len(lanes) > maxLanes:
		return Plan{}, &PlanError{"too_many_lanes", fmt.Sprint(len(lanes))}
	}
	sort.Slice(lanes, func(i, j int) bool { return lanes[i].ID < lanes[j].ID })
	ordered, err := orderLanes(lanes)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Text: string(text), Lanes: ordered}, nil
}

func orderLanes(lanes []Lane) ([]Lane, error) {
	byID := map[string]Lane{}
	for _, l := range lanes {
		byID[l.ID] = l
	}
	for _, l := range lanes {
		for _, a := range l.After {
			if _, ok := byID[a]; !ok {
				return nil, &PlanError{"bad_after", l.ID + " waits for " + a}
			}
		}
	}
	var out []Lane
	done := map[string]bool{}
	for len(out) < len(lanes) {
		progressed := false
		for _, l := range lanes {
			if done[l.ID] {
				continue
			}
			ready := true
			for _, a := range l.After {
				ready = ready && done[a]
			}
			if ready {
				done[l.ID] = true
				out = append(out, l)
				progressed = true
			}
		}
		if !progressed {
			return nil, &PlanError{"cycle", "the After: lines form a cycle"}
		}
	}
	return out, nil
}
