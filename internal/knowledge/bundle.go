package knowledge

// Bundle is the deterministic evidence bundle of a finished run (K12 step 1). Every element has a RefID that the
// server's resolver can resolve in the same org. The resolver exposes these Fields so template params can be checked:
//
//	verification_evidence: command, outcome, head, round, failed_round (= round), fixed_head (= head), files
//	merge_request:         iid, merge_commit
//	review_finding:        severity, title_sha8, round, status
//	run:                   verdict, head
type Bundle struct {
	RunID, ProjectID, IssueKey, Head string
	Merged                           bool
	MR                               *BundleMR
	Acceptance                       []BundleAcceptance
	Verification                     []BundleVerification
	Review                           *BundleReview
	Findings                         []BundleFinding
}
type BundleMR struct {
	RefID       string
	IID         int
	MergeCommit string
}
type BundleAcceptance struct {
	Idx  int
	Text string
}
type BundleVerification struct {
	RefID, Command string
	Round          int
	Outcome        string // pass | fail
	Head           string
	Files          []string
}
type BundleReview struct {
	RefID, Verdict, Head string
}
type BundleFinding struct {
	RefID, Severity, Title string
	Round                  int
	Status                 string
}
