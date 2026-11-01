package hostfake

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"orqen/internal/catherd"
)

// materialize finalizes every dispatch whose record is due.
func (c *Catherd) materialize(st *State, sc *Scenario, now time.Time) {
	delay := time.Duration(sc.RecordDelayMs) * time.Millisecond
	ids := make([]string, 0, len(st.Runs))
	for id := range st.Runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := st.Runs[id]
		for _, d := range r.Disps {
			if !d.Finalized && !now.Before(d.AdmittedAt.Add(delay)) {
				c.finalize(st, r, d, sc, now, "")
			}
		}
	}
}

func writeFiles(root string, files map[string]string) []string {
	var changed []string
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if content == DeleteFile {
			if os.Remove(full) == nil {
				changed = append(changed, rel)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			continue
		}
		if os.WriteFile(full, []byte(content), 0o644) == nil {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed
}

func laneFile(l LaneSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n", l.ID, l.Title)
	fmt.Fprintf(&b, "Owns: %s\n", strings.Join(l.Owns, ", "))
	if len(l.After) > 0 {
		fmt.Fprintf(&b, "After: %s\n", strings.Join(l.After, ", "))
	}
	b.WriteString("Kind: repo_code\nDifficulty: build\nFast check: sh scripts/check.sh\n\nImplement the lane as the plan says.\n")
	return b.String()
}

// finalize writes the role's reply and record. cancelledStatus is "cancelled" for runs cancel.
func (c *Catherd) finalize(st *State, r *Run, d *Disp, sc *Scenario, now time.Time, forced string) {
	d.Finalized = true
	status, reply := "ok", ""
	var changed []string
	if forced == "" && sc != nil {
		st2, ok := seqAt(sc.RoleStatuses, d.Name, d.NameIndex)
		if ok && st2 != "" {
			status = st2
		}
		if status == "ok" {
			switch d.Role {
			case "architect":
				reply = c.architectReply(r, sc)
			case "worker":
				reply, changed = c.workerReply(r, d, sc)
			case "verifier":
				if strings.HasPrefix(d.Name, "kverify-") {
					reply = kverifyReply(sc, d)
				} else {
					reply = verifierReply(sc, d)
				}
			case "writer":
				reply = writerReply(sc, d)
			case "reviewer":
				reply = reviewerReply(sc, d)
			default:
				reply = "Done.\nSTATUS: complete — done"
			}
		} else if status == "failed" || status == "timeout" {
			reply = "The role could not finish.\nSTATUS: blocked — " + status
		}
	} else if forced != "" {
		status = forced
	}
	rung, from := d.Rung, (*string)(nil)
	if sc != nil && forced == "" {
		if to := sc.FailoverTo[d.Role]; to != "" && to != d.Rung {
			f := d.Rung
			rung, from = to, &f
		}
	}
	p, _ := catherd.ParseRung(rung)
	rel := filepath.Join("roles", d.Name, d.DispatchID, "reply.md")
	if reply != "" {
		full := filepath.Join(r.Dir, rel)
		_ = os.MkdirAll(filepath.Dir(full), 0o700)
		_ = os.WriteFile(full, []byte(reply), 0o600)
	}
	var lane *string
	if d.Lane != "" {
		l := d.Lane
		lane = &l
	}
	thr := d.Thread
	rec := catherd.RunRecord{Schema: 1, RunID: r.ID, DispatchID: d.DispatchID, Name: d.Name, Role: d.Role, Lane: lane, Backend: p.Backend, Rung: rung, Attempt: d.NameIndex + 1, FailoverFrom: from, Thread: &thr,
		Status: status, StartedAt: ts(d.AdmittedAt), EndedAt: ts(now), Secs: now.Sub(d.AdmittedAt).Seconds(), Tokens: catherd.Tokens{Input: 1000, Cached: 400, Output: 200},
		ChangedOwned: nonNil(changed), Violations: []string{}, Access: map[string]string{"worker": "workspace-write"}[d.Role], ReplyPath: rel, Host: "codex"}
	if rec.Access == "" {
		rec.Access = "read-only"
	}
	if reply != "" {
		if s, why := parseStatus(reply); s != "" {
			rec.ReplyStatus, rec.ReplyWhy = &s, &why
		}
	}
	r.Records = append(r.Records, rec)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func parseStatus(reply string) (status, why string) {
	lines := strings.Split(strings.TrimRight(reply, "\n "), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	for _, s := range []string{"complete", "partial", "blocked", "refused", "flaky"} {
		if rest, ok := strings.CutPrefix(last, "STATUS: "+s); ok {
			return s, strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(rest), "—–-"))
		}
	}
	return "", ""
}

func (c *Catherd) architectReply(r *Run, sc *Scenario) string {
	var plan strings.Builder
	fmt.Fprintf(&plan, "# Plan: %s\n\nGoal: %s\n\n## M1\n", r.Title, sc.Goal)
	for _, l := range sc.Lanes {
		fmt.Fprintf(&plan, "- %s: %s (owns %s)\n", l.ID, l.Title, strings.Join(l.Owns, ", "))
		_ = os.WriteFile(filepath.Join(r.Dir, "lanes", l.ID+".md"), []byte(laneFile(l)), 0o600)
	}
	plan.WriteString("\nFull check: sh scripts/check.sh\n")
	_ = os.WriteFile(filepath.Join(r.Dir, "plan.md"), []byte(plan.String()), 0o600)
	return "M1 with the lanes above.\nSTATUS: complete — plan written"
}

func (c *Catherd) workerReply(r *Run, d *Disp, sc *Scenario) (string, []string) {
	var (
		reply string
		edits map[string]string
	)
	switch {
	case sc.WorkerFn != nil:
		reply, edits = sc.WorkerFn(d.NameIndex, r.Repo)
	case sc.lies(d.NameIndex):
		edits = map[string]string{"src/feature.txt": "broken\n"}
		reply = "Implemented the change. All tests pass.\nSTATUS: complete — all tests pass"
	case sc.NoChange:
		reply = "Nothing to change.\nSTATUS: complete — no change needed"
	default:
		edits = sc.workerEdits(d.NameIndex)
		reply = "Implemented the change.\nSTATUS: complete — implemented"
	}
	changed := writeFiles(r.Repo, edits)
	if sc.WorkerPushAttempt && sc.PushEnv != nil {
		cmd := exec.Command("git", "push", "origin", "HEAD")
		cmd.Dir = r.Repo
		cmd.Env = sc.PushEnv()
		_ = cmd.Run()
	}
	return reply, changed
}

func verifierReply(sc *Scenario, d *Disp) string {
	switch sc.verdict(d.Index) {
	case "FAIL":
		return "VERDICT: FAIL — the acceptance check does not hold\nSTATUS: complete — checked"
	case "BLOCKED":
		return "VERDICT: BLOCKED: environment — docker info fails\nSTATUS: blocked — environment"
	}
	return "VERDICT: PASS\nSTATUS: complete — checked"
}

func reviewerReply(sc *Scenario, d *Disp) string {
	if sc.ReviewerFn != nil {
		return sc.ReviewerFn(d.Index)
	}
	if d.Index < sc.MalformedReviews {
		return "I looked at the change and it seems fine to me.\nSTATUS: complete — reviewed"
	}
	spec := sc.review(d.Index)
	if spec.Verdict == "" {
		spec.Verdict = "approved"
	}
	findings := make([]map[string]any, 0, len(spec.Findings))
	for i, f := range spec.Findings {
		id := f.ID
		if id == "" {
			id = fmt.Sprintf("F%d", i+1)
		}
		side := f.Side
		if side == "" {
			side = "new"
		}
		findings = append(findings, map[string]any{"id": id, "severity": f.Severity, "file": f.File, "line": f.Line, "side": side, "title": f.Title, "body": f.Body, "acceptance_ref": f.AcceptanceRef})
	}
	payload, _ := json.Marshal(map[string]any{"verdict": spec.Verdict, "scope": spec.Scope, "findings": findings})
	return "Review of the change against the acceptance criteria.\n\nORQEN-REVIEW v1\n" + string(payload) + "\n\nSTATUS: complete — reviewed"
}

func pickReply(list []string, i int) (string, bool) {
	if len(list) == 0 {
		return "", false
	}
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i], true
}

var refLine = regexp.MustCompile(`ref_id=(\S+)`)

// writerReply answers the knowledge writer: scripted, or one project record citing the first reference of the bundle.
func writerReply(sc *Scenario, d *Disp) string {
	if r, ok := pickReply(sc.WriterReply, d.NameIndex); ok {
		return r
	}
	ref := "none"
	if m := refLine.FindStringSubmatch(d.Brief); m != nil {
		ref = strings.Trim(m[1], ".,;`\"'")
	}
	payload, _ := json.Marshal(map[string]any{"records": []any{map[string]any{"type": "project", "scope_kind": "project", "key": "fake-rule", "title": "A rule from the run", "body": "Learned from the merged run.",
		"claims": []any{map[string]any{"text": "The run produced a reviewable change", "evidence": []any{map[string]any{"ref_id": ref, "excerpt": "evidence"}}}}}}})
	return "Drafted from the bundle.\n\nORQEN-KNOWLEDGE v1\n" + string(payload) + "\n\nSTATUS: complete — drafted"
}

var claimLine = regexp.MustCompile(`claim_id=(\S+):`)

// kverifyReply answers the knowledge verifier: scripted, or "supported" for every claim id in the brief.
func kverifyReply(sc *Scenario, d *Disp) string {
	if r, ok := pickReply(sc.KVerifyReply, d.NameIndex); ok {
		return r
	}
	claims := []map[string]string{}
	for _, m := range claimLine.FindAllStringSubmatch(d.Brief, -1) {
		claims = append(claims, map[string]string{"claim_id": m[1], "verdict": "supported", "reason": ""})
	}
	payload, _ := json.Marshal(map[string]any{"claims": claims})
	return "Checked.\n\nORQEN-KVERIFY v1\n" + string(payload) + "\n\nSTATUS: complete — checked"
}
