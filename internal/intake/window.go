package intake

import "time"

const (
	// OverlapMinutes is added to every incremental window so a slow poll or a clock skew loses nothing.
	OverlapMinutes = 5
	// MaxWindowMinutes is one week; a longer gap becomes a full scan.
	MaxWindowMinutes = 10080
)

// Window returns the relative "updated >= -Nm" size for the next poll, or full=true when the whole filter must be
// scanned: no previous success, a gap above one week, or reconcileEvery polls since the last full scan.
func Window(lastSuccess *time.Time, now time.Time, pollsSinceFull, reconcileEvery int) (minutes int, full bool) {
	if lastSuccess == nil || (reconcileEvery > 0 && pollsSinceFull >= reconcileEvery) {
		return 0, true
	}
	gap := now.Sub(*lastSuccess)
	if gap < 0 {
		gap = 0
	}
	n := int((gap+time.Minute-1)/time.Minute) + OverlapMinutes
	if n > MaxWindowMinutes {
		return 0, true
	}
	return n, false
}
