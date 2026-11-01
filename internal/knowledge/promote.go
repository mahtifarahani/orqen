package knowledge

type Outcome string

const (
	AutoVerify  Outcome = "auto_verify"
	AwaitReview Outcome = "await_review"
	Skip        Outcome = "skip"
)

type Policy string

const (
	PolicyOff    Policy = "off"
	PolicyManual Policy = "manual"
	PolicyAuto   Policy = "auto"
)

// Decide applies the promotion policy of K11. Unknown policy values behave like manual (never auto).
func Decide(policy string, never []string) Outcome {
	switch Policy(policy) {
	case PolicyOff:
		return Skip
	case PolicyAuto:
		if len(never) == 0 {
			return AutoVerify
		}
	}
	return AwaitReview
}

// DecideReasons is Decide plus the never-auto codes that apply to the decision, including policy_not_auto.
func DecideReasons(policy string, never []string) (Outcome, []string) {
	out := append([]string{}, never...)
	if Policy(policy) != PolicyAuto {
		out = append(out, NAPolicyNotAuto)
	}
	return Decide(policy, never), out
}

// UserVerifyBlockers returns the failing claim ids that a user verification still has to cover with an attestation.
func UserVerifyBlockers(g GateResult, attested map[string]bool) []string {
	var out []string
	for _, c := range g.Claims {
		if c.Verdict != VerdictSupported && !attested[c.ClaimID] {
			out = append(out, c.ClaimID)
		}
	}
	return out
}
