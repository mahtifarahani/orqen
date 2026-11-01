package triage

import "orqen/internal/decision"

// Thresholds for reading Jev's answers.
const (
	ChoiceMin  = 0.8
	NoulTrue   = 0.7
	NoulFalse  = 0.3
	unsureWord = "unsure"
)

// JevAnswers are Jev's answers after the thresholds: a word, or "unsure".
type JevAnswers struct {
	Clarity     string // clear | ambiguous | contradictory | unsure
	Scope       string // single_repo | multi_repo | unsure (unclear is unsure)
	Testable    string // true | false | unsure
	AlreadyDone string // true | false | unsure
	Areas       map[string]string
}

func readChoice(a decision.Answer, ok bool) string {
	if !ok || a.Probabilities[a.Choice] < ChoiceMin {
		return unsureWord
	}
	return a.Choice
}
func readNoul(a decision.Answer, ok bool) string {
	switch {
	case !ok:
		return unsureWord
	case a.Noul >= NoulTrue:
		return "true"
	case a.Noul <= NoulFalse:
		return "false"
	}
	return unsureWord
}

// ReadJev applies the thresholds to a validated response.
func ReadJev(r decision.Response) JevAnswers {
	get := func(id string) (decision.Answer, bool) { a, ok := r.Answers[id]; return a, ok }
	c, ok := get("clarity")
	out := JevAnswers{Clarity: readChoice(c, ok), Areas: map[string]string{}}
	s, ok := get("scope")
	out.Scope = readChoice(s, ok)
	if out.Scope == "unclear" {
		out.Scope = unsureWord
	}
	t, ok := get("testable")
	out.Testable = readNoul(t, ok)
	d, ok := get("already_done")
	out.AlreadyDone = readNoul(d, ok)
	for _, p := range [][2]string{{"touches_auth", "auth"}, {"touches_payment", "payment"}, {"touches_migration", "migration"}, {"touches_data_deletion", "data_deletion"}, {"touches_ci_config", "ci_config"}, {"cross_service", "cross_service"}} {
		a, ok := get(p[0])
		out.Areas[p[1]] = readNoul(a, ok)
	}
	return out
}

// TrueAreas lists the areas Jev flagged.
func (j JevAnswers) TrueAreas() []string {
	var out []string
	for _, a := range Areas {
		if j.Areas[a] == "true" {
			out = append(out, a)
		}
	}
	return out
}

// Sources of a decision.
const (
	SourceGuard      = "guard"
	SourceJev        = "jev"
	SourceFixedRules = "fixed_rules"
)

// DecideInput gathers the evidence of a triage.
type DecideInput struct {
	Findings []Finding
	// Jev is nil when Jev was not used or its answer was unusable (outage, invalid, no key, off).
	Jev  *JevAnswers
	Risk RiskInput
	// Fixed rules (SPEC section 9 fallback).
	AcceptanceSource string // field | section | answer (given by the user) | none
	AcceptanceItems  int
	HasApprovedTest  bool
	// UserClarified is set when the user answered a clarification: Jev's clarity and testability stops no longer apply
	// and the fixed rules accept the answer (the plan gate still asks for sensitive work).
	UserClarified bool
	// IgnoreAlreadyDone is set when the user chose to continue although the work looked done.
	IgnoreAlreadyDone bool
}
type Decision struct {
	Outcome      string
	Source       string
	Reason       string
	QuestionCode string
	Params       map[string]any
	Risk         string
	RiskReasons  []string
	Reasons      []string
}

// Decide is the single place the triage outcome is chosen. Guards decide first. Jev may only escalate: it can add
// risk areas or stop a run, never remove a deterministic finding. Anything unsure falls back to the fixed rules.
func Decide(in DecideInput) Decision {
	ri := in.Risk
	if in.Jev != nil {
		ri.JevAreas = append(append([]string{}, ri.JevAreas...), in.Jev.TrueAreas()...)
	}
	risk := Risk(ri)
	d := Decision{Risk: risk.Class, RiskReasons: risk.Reasons}
	for _, f := range in.Findings {
		d.Reasons = append(d.Reasons, "guard:"+f.QuestionCode)
	}
	if len(in.Findings) > 0 {
		f := in.Findings[0]
		d.Outcome, d.Source, d.Reason, d.QuestionCode, d.Params = f.Outcome, SourceGuard, f.Reason, f.QuestionCode, f.Params
		return d
	}
	stop := func(reason, code string) Decision {
		d.Outcome, d.Source, d.Reason, d.QuestionCode = OutcomeNeedsInput, SourceJev, reason, code
		d.Reasons = append(d.Reasons, "jev:"+code)
		return d
	}
	if j := in.Jev; j != nil {
		switch {
		case j.Clarity == "ambiguous" && !in.UserClarified:
			return stop(ReasonAmbiguousBehavior, "ambiguous_requirements")
		case j.Clarity == "contradictory" && !in.UserClarified:
			return stop(ReasonAmbiguousBehavior, "contradictory_requirements")
		case j.Scope == "multi_repo":
			return stop(ReasonProjectMapping, "multi_repo")
		case j.Testable == "false" && !in.UserClarified:
			return stop(ReasonAmbiguousBehavior, "untestable_acceptance")
		case j.AlreadyDone == "true" && !in.IgnoreAlreadyDone:
			return stop(ReasonAmbiguousBehavior, "already_addressed_confirm")
		case (j.Clarity == "clear" || in.UserClarified) && (j.Testable == "true" || in.UserClarified) && j.Scope == "single_repo":
			d.Outcome, d.Source = OutcomeExecute, SourceJev
			d.Reasons = append(d.Reasons, "jev:clear")
			return d
		}
		d.Reasons = append(d.Reasons, "jev:unsure")
	}
	d.Source = SourceFixedRules
	if in.UserClarified && in.HasApprovedTest {
		d.Outcome = OutcomeExecute
		d.Reasons = append(d.Reasons, "fixed:user_clarified")
		return d
	}
	if (in.AcceptanceSource == "field" || in.AcceptanceSource == "section" || in.AcceptanceSource == "answer") && in.AcceptanceItems >= 1 && d.Risk == RiskLow && in.HasApprovedTest {
		d.Outcome = OutcomeExecute
		d.Reasons = append(d.Reasons, "fixed:execute")
		return d
	}
	d.Outcome, d.Reason, d.QuestionCode = OutcomeNeedsInput, ReasonAmbiguousBehavior, "needs_clarification"
	d.Reasons = append(d.Reasons, "fixed:needs_clarification")
	return d
}
