// Package kauthor is the pure state machine of the detached roles knowledge author (S7). It knows nothing about
// MongoDB, catherd or the host: the server gathers Facts and executes the Step that Next returns.
package kauthor

// Session states.
const (
	Queued            = "queued"
	Starting          = "starting"
	WriterDispatching = "writer_dispatching"
	WriterRunning     = "writer_running"
	WriterDone        = "writer_done"
	KVerifyDispatch   = "kverify_dispatching"
	KVerifyRunning    = "kverify_running"
	KVerifyDone       = "kverify_done"
	Applied           = "applied"
	Failed            = "failed"
	CancelRequested   = "cancel_requested"
	Cancelled         = "cancelled"
)

// States lists every state in order.
var States = []string{Queued, Starting, WriterDispatching, WriterRunning, WriterDone, KVerifyDispatch, KVerifyRunning,
	KVerifyDone, Applied, Failed, CancelRequested, Cancelled}

// Fail codes of a session.
const (
	CodeHostUnavailable    = "host_unavailable"
	CodeCatherdUnavailable = "catherd_unavailable"
	CodeWriterDisabled     = "writer_role_disabled"
	CodeVerifierDisabled   = "verifier_role_disabled"
	CodeDispatchUncertain  = "dispatch_uncertain"
	CodeReplyUnparseable   = "reply_unparseable"
	CodeRoleFailed         = "role_failed"
	CodeTimeout            = "timeout"
	CodeRestoredIncomplete = "restored_incomplete"
	CodeOriginMissing      = "origin_missing"
	CodeCancelled          = "cancelled"
)

// Codes lists every fail code.
var Codes = []string{CodeHostUnavailable, CodeCatherdUnavailable, CodeWriterDisabled, CodeVerifierDisabled,
	CodeDispatchUncertain, CodeReplyUnparseable, CodeRoleFailed, CodeTimeout, CodeRestoredIncomplete, CodeOriginMissing, CodeCancelled}

// MaxHostFailures is how many failed host starts a session tolerates before it fails with host_unavailable.
const MaxHostFailures = 5

// MaxWriterTries is the number of writer dispatches allowed (the first plus one retry after an unparseable reply).
const MaxWriterTries = 2

// Terminal reports whether a state is final.
func Terminal(state string) bool {
	return state == Applied || state == Failed || state == Cancelled
}
