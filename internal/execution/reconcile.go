package execution

import (
	"sort"
	"time"

	"orqen/internal/catherd"
	"orqen/internal/codexhost"
)

// ReconcileDispatch finds the dispatch a lost directive turn admitted: the earliest dispatch of that name admitted
// at or after `since` (the moment Orqen marked the role execution dispatching) that no other role execution has
// claimed.
func ReconcileDispatch(view catherd.RunView, name string, since time.Time, claimed map[string]bool) (dispatchID string, found bool) {
	var cands []catherd.Dispatch
	for _, d := range view.Dispatches {
		if d.Name != name || claimed[d.DispatchID] {
			continue
		}
		if t := d.AdmittedTime(); !t.IsZero() && !t.Before(since.Truncate(time.Millisecond)) {
			cands = append(cands, d)
		}
	}
	if len(cands) == 0 {
		return "", false
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].AdmittedTime().Before(cands[j].AdmittedTime()) })
	return cands[0].DispatchID, true
}

// DispatchSafeToRetry reads a thread's history for a directive whose outcome is unknown.
//   - known=false: the history cannot decide (it is missing, or the directive's turn is still running)
//   - safe=true, known=true: the directive's turn never started, or ended without a catherd call
//   - safe=false, known=true: a catherd call of that turn completed (the dispatch happened)
//
// A nil history (unreadable thread) is never known.
func DispatchSafeToRetry(history []codexhost.Turn, directiveID string) (safe, known bool) {
	if history == nil {
		return false, false
	}
	turn, ok := codexhost.TurnForDirective(history, directiveID)
	if !ok {
		return true, true
	}
	if len(codexhost.ToolCallsOf(turn)) > 0 {
		return false, true
	}
	for _, it := range turn.Items {
		if it.Type == "mcpToolCall" && it.Server == "catherd" {
			return false, false // started, not completed: unknown
		}
	}
	if turn.Status == "inProgress" {
		return false, false
	}
	return true, true
}
