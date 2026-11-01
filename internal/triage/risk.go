package triage

import "sort"

// Risk classes in increasing order.
const (
	RiskLow       = "low"
	RiskMedium    = "medium"
	RiskSensitive = "sensitive"
)

func riskRank(c string) int {
	switch c {
	case RiskSensitive:
		return 2
	case RiskMedium:
		return 1
	}
	return 0
}

// RiskInput is what the risk class is computed from.
type RiskInput struct {
	// Text is the summary, acceptance and description (never comments).
	Text []string
	// AreaClass maps each area to its class from policy risk.area.<area>; missing means sensitive.
	AreaClass map[string]string
	// ReviewMatches are path tokens matching a protected "review" pattern.
	ReviewMatches   []string
	AcceptanceItems int
	LinkCount       int
	// JevAreas are the areas Jev flagged (a noul that came back true).
	JevAreas []string
}
type RiskResult struct {
	Class   string
	Reasons []string
	Areas   []string
}

// Risk is the maximum over the detected areas, protected review paths and size signals. Jev can only add areas.
func Risk(in RiskInput) RiskResult {
	class := RiskLow
	var reasons []string
	raise := func(c, reason string) {
		if riskRank(c) > riskRank(class) {
			class = c
		}
		reasons = append(reasons, reason)
	}
	areas := map[string]bool{}
	for _, a := range DetectAreas(in.Text...) {
		areas[a] = true
	}
	for _, a := range in.JevAreas {
		areas[a] = true
	}
	var names []string
	for a := range areas {
		names = append(names, a)
	}
	sort.Strings(names)
	for _, a := range names {
		c := in.AreaClass[a]
		if c == "" {
			c = RiskSensitive
		}
		if c != RiskLow {
			raise(c, "area:"+a)
		} else {
			reasons = append(reasons, "area:"+a+":low")
		}
	}
	if len(in.ReviewMatches) > 0 {
		raise(RiskSensitive, "protected_review")
	}
	if in.AcceptanceItems > 8 {
		raise(RiskMedium, "many_acceptance_items")
	}
	if in.LinkCount >= 3 {
		raise(RiskMedium, "many_links")
	}
	return RiskResult{Class: class, Reasons: reasons, Areas: names}
}
