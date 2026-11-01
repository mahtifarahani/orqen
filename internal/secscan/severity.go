package secscan

// Raw severities of the scanner, highest first.
var rawRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1, "info": 0}

// Review severities (internal/review) the raw ones map to.
const (
	SevBlocker = "blocker"
	SevMajor   = "major"
	SevMinor   = "minor"
	SevNit     = "nit"
)

// Map converts a raw severity to the review vocabulary: critical to blocker, high to major, medium to minor, and
// low and info to nit. An unknown value maps to major so it is never silently demoted.
func Map(raw string) string {
	switch raw {
	case "critical":
		return SevBlocker
	case "high":
		return SevMajor
	case "medium":
		return SevMinor
	case "low", "info":
		return SevNit
	}
	return SevMajor
}

// Blocking reports whether a finding of this raw severity blocks under the threshold (critical, high, medium, low).
// info never blocks; an unknown raw value blocks.
func Blocking(raw, threshold string) bool {
	r, ok := rawRank[raw]
	if !ok {
		return true
	}
	t, ok := rawRank[threshold]
	if !ok {
		t = rawRank["high"]
	}
	return r >= t && r > 0
}

// ValidSeverity reports whether raw is a known raw severity.
func ValidSeverity(raw string) bool { _, ok := rawRank[raw]; return ok }
