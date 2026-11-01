package knowledge

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxBundleRender bounds the rendered bundle handed to the writer role.
const MaxBundleRender = 16 << 10

// BundleRefs maps every ref_id of the bundle to its reference kind (the map ParseWriterReply validates against).
func BundleRefs(b Bundle) map[string]string {
	m := map[string]string{}
	if b.MR != nil && b.MR.RefID != "" {
		m[b.MR.RefID] = RefMergeRequest
	}
	for _, v := range b.Verification {
		if v.RefID != "" {
			m[v.RefID] = RefVerificationEvidence
		}
	}
	if b.Review != nil && b.Review.RefID != "" {
		m[b.Review.RefID] = RefRun
	}
	for _, f := range b.Findings {
		if f.RefID != "" {
			m[f.RefID] = RefReviewFinding
		}
	}
	return m
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}

// RenderBundle renders the bundle as plain lines, deterministic and at most MaxBundleRender bytes. Every line that
// can be cited starts with its ref_id.
func RenderBundle(b Bundle) string {
	var sb strings.Builder
	line := func(format string, a ...any) {
		l := fmt.Sprintf(format, a...) + "\n"
		if sb.Len()+len(l) <= MaxBundleRender {
			sb.WriteString(l)
		}
	}
	line("issue: %s", oneLine(b.IssueKey, 80))
	line("head: %s merged: %v", oneLine(b.Head, 64), b.Merged)
	for _, a := range b.Acceptance {
		line("acceptance %d: %s", a.Idx, oneLine(a.Text, 300))
	}
	if b.MR != nil {
		line("ref_id=%s kind=%s merge_request iid=%d merge_commit=%s", b.MR.RefID, RefMergeRequest, b.MR.IID, oneLine(b.MR.MergeCommit, 64))
	}
	vs := append([]BundleVerification(nil), b.Verification...)
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].RefID < vs[j].RefID })
	for _, v := range vs {
		files := append([]string(nil), v.Files...)
		sort.Strings(files)
		if len(files) > 10 {
			files = files[:10]
		}
		line("ref_id=%s kind=%s command=%s round=%d outcome=%s head=%s files=%s", v.RefID, RefVerificationEvidence,
			oneLine(v.Command, 120), v.Round, v.Outcome, oneLine(v.Head, 64), strings.Join(files, ","))
	}
	if b.Review != nil {
		line("ref_id=%s kind=%s review verdict=%s head=%s", b.Review.RefID, RefRun, b.Review.Verdict, oneLine(b.Review.Head, 64))
	}
	fs := append([]BundleFinding(nil), b.Findings...)
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].RefID < fs[j].RefID })
	for _, f := range fs {
		line("ref_id=%s kind=%s severity=%s round=%d status=%s title=%s", f.RefID, RefReviewFinding, f.Severity, f.Round, f.Status, oneLine(f.Title, 200))
	}
	return sb.String()
}
