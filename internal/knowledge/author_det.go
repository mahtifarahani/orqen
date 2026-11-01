package knowledge

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Template codes of the deterministic author (K14). Business templates do not exist.
const (
	TplAcceptanceVerified = "task.acceptance_verified"
	TplTaskMerged         = "task.merged"
	TplFixAfterFailure    = "experience.fix_after_failure"
	TplFindingFixed       = "experience.finding_fixed"
	TplCommandPasses      = "project.command_passes"
)

var TemplateCodes = []string{TplAcceptanceVerified, TplTaskMerged, TplFixAfterFailure, TplFindingFixed, TplCommandPasses}

type DraftRecord struct {
	Type      Type
	ScopeKind string
	ScopeKey  string
	Key       string
	Title     string
	Body      string
	Claims    []Claim
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if len(s) < 2 {
		s = "x" + s
	}
	return s
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func joinSorted(xs []string) string {
	c := append([]string{}, xs...)
	sort.Strings(c)
	out := c[:0]
	for i, x := range c {
		if i == 0 || x != c[i-1] {
			out = append(out, x)
		}
	}
	return strings.Join(out, ",")
}

// DraftFromBundle turns a run's evidence bundle into template-based draft records. It never emits business
// records and never uses a model; every claim carries a fact and evidence refs.
func DraftFromBundle(b Bundle) []DraftRecord {
	var out []DraftRecord
	run8 := short(slug(b.RunID), 12)
	taskScope, projScope := "task:"+b.RunID, "project:"+b.ProjectID
	issue := b.IssueKey
	if issue == "" {
		issue = b.RunID
	}

	// Task record: acceptance items verified at the head, then the merge.
	var tclaims []Claim
	var passAtHead []BundleVerification
	for _, v := range b.Verification {
		if v.Outcome == "pass" && v.Head == b.Head && b.Head != "" {
			passAtHead = append(passAtHead, v)
		}
	}
	if len(passAtHead) > 0 && b.Review != nil && b.Review.Verdict == "approve" && b.Review.Head == b.Head {
		var cmds []string
		var ev []Evidence
		for _, v := range passAtHead {
			cmds = append(cmds, v.Command)
			ev = append(ev, Evidence{RefKind: RefVerificationEvidence, RefID: v.RefID})
		}
		ev = append(ev, Evidence{RefKind: RefRun, RefID: b.Review.RefID})
		for _, a := range b.Acceptance {
			tclaims = append(tclaims, Claim{
				ID:   "cl_" + strconv.Itoa(len(tclaims)+1),
				Text: fmt.Sprintf("Acceptance item %d was verified at head %s by: %s", a.Idx, short(b.Head, 8), joinSorted(cmds)),
				Template: &Template{Code: TplAcceptanceVerified, Params: map[string]string{
					"item_idx": strconv.Itoa(a.Idx), "head": b.Head, "commands": joinSorted(cmds)}},
				Fact:     &Fact{Subject: "issue:" + issue, Predicate: fmt.Sprintf("acceptance_item_%d", a.Idx), Value: "verified@" + short(b.Head, 8)},
				Evidence: ev,
			})
		}
	}
	if b.Merged && b.MR != nil {
		tclaims = append(tclaims, Claim{
			ID:   "cl_" + strconv.Itoa(len(tclaims)+1),
			Text: fmt.Sprintf("Merge request !%d was merged as %s", b.MR.IID, short(b.MR.MergeCommit, 8)),
			Template: &Template{Code: TplTaskMerged, Params: map[string]string{
				"iid": strconv.Itoa(b.MR.IID), "merge_commit": b.MR.MergeCommit}},
			Fact:     &Fact{Subject: "issue:" + issue, Predicate: "merged_mr", Value: fmt.Sprintf("!%d@%s", b.MR.IID, short(b.MR.MergeCommit, 8))},
			Evidence: []Evidence{{RefKind: RefMergeRequest, RefID: b.MR.RefID}},
		})
	}
	if len(tclaims) > 0 {
		out = append(out, DraftRecord{Type: TypeTask, ScopeKind: "task", ScopeKey: taskScope, Key: "outcome",
			Title: "Task outcome " + issue, Body: "Facts recorded from the run evidence of " + issue + ".", Claims: tclaims})
	}

	// Experience: a failed command later fixed.
	byCmd := map[string][]BundleVerification{}
	var cmds []string
	for _, v := range b.Verification {
		if _, ok := byCmd[v.Command]; !ok {
			cmds = append(cmds, v.Command)
		}
		byCmd[v.Command] = append(byCmd[v.Command], v)
	}
	sort.Strings(cmds)
	for _, cmd := range cmds {
		vs := byCmd[cmd]
		sort.SliceStable(vs, func(i, j int) bool { return vs[i].Round < vs[j].Round })
		var failed *BundleVerification
		var fixed *BundleVerification
		for i := range vs {
			if vs[i].Outcome == "fail" && failed == nil {
				failed = &vs[i]
			}
			if vs[i].Outcome == "pass" && failed != nil && vs[i].Round > failed.Round && vs[i].Head != failed.Head {
				fixed = &vs[i]
				break
			}
		}
		if failed == nil || fixed == nil {
			continue
		}
		out = append(out, DraftRecord{Type: TypeExperience, ScopeKind: "project", ScopeKey: projScope,
			Key: "fix-" + slug(cmd) + "-" + run8, Title: "Fix after failure: " + cmd,
			Body: "The command failed in round " + strconv.Itoa(failed.Round) + " and passed after a fix.",
			Claims: []Claim{{ID: "cl_1",
				Text: fmt.Sprintf("%s failed in round %d and passed at head %s after changing: %s", cmd, failed.Round, short(fixed.Head, 8), joinSorted(fixed.Files)),
				Template: &Template{Code: TplFixAfterFailure, Params: map[string]string{
					"command": cmd, "failed_round": strconv.Itoa(failed.Round), "outcome": "pass", "fixed_head": fixed.Head, "files": joinSorted(fixed.Files)}},
				Fact: &Fact{Subject: "command:" + cmd, Predicate: "fix_after_failure:" + run8, Value: "pass@" + short(fixed.Head, 8)},
				Evidence: []Evidence{{RefKind: RefVerificationEvidence, RefID: failed.RefID},
					{RefKind: RefVerificationEvidence, RefID: fixed.RefID}}}}})
	}
	// Experience: a fixed review finding.
	for _, f := range b.Findings {
		if f.Status != "fixed" {
			continue
		}
		t8 := short(SHA(NormText(f.Title)), 8)
		out = append(out, DraftRecord{Type: TypeExperience, ScopeKind: "project", ScopeKey: projScope,
			Key: "finding-" + t8 + "-" + run8, Title: "Review finding fixed: " + f.Title,
			Body: "A review finding of severity " + f.Severity + " was fixed in round " + strconv.Itoa(f.Round) + ".",
			Claims: []Claim{{ID: "cl_1",
				Text: fmt.Sprintf("A %s review finding (%s) was fixed in round %d", f.Severity, t8, f.Round),
				Template: &Template{Code: TplFindingFixed, Params: map[string]string{
					"severity": f.Severity, "title_sha8": t8, "round": strconv.Itoa(f.Round)}},
				Fact:     &Fact{Subject: "finding:" + t8, Predicate: "fixed:" + run8, Value: "round" + strconv.Itoa(f.Round)},
				Evidence: []Evidence{{RefKind: RefReviewFinding, RefID: f.RefID}}}}})
	}
	// Project: a command passes at the merged head.
	if b.Merged && b.MR != nil {
		for _, v := range passAtHead {
			out = append(out, DraftRecord{Type: TypeProject, ScopeKind: "project", ScopeKey: projScope,
				Key: "command-" + slug(v.Command) + "-passes", Title: "Command passes: " + v.Command,
				Body: "Observed passing in the verification of a merged task.",
				Claims: []Claim{{ID: "cl_1",
					Text: fmt.Sprintf("%s passes (merge commit %s)", v.Command, short(b.MR.MergeCommit, 8)),
					Template: &Template{Code: TplCommandPasses, Params: map[string]string{
						"command": v.Command, "merge_commit": b.MR.MergeCommit}},
					Fact: &Fact{Subject: "command:" + v.Command, Predicate: "passes", Value: "true"},
					Evidence: []Evidence{{RefKind: RefVerificationEvidence, RefID: v.RefID},
						{RefKind: RefMergeRequest, RefID: b.MR.RefID}}}}})
		}
	}
	return out
}
