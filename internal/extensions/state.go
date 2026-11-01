package extensions

import "fmt"

// Status is the lifecycle state of one extension in one org.
type Status string

const (
	StatusAbsent          Status = "absent"
	StatusRegistered      Status = "registered"
	StatusLicenseRequired Status = "license_required"
	StatusDetected        Status = "detected"
	StatusDetectFailed    Status = "detect_failed"
	StatusTested          Status = "tested"
	StatusTestFailed      Status = "test_failed"
	StatusEnabled         Status = "enabled"
	StatusDisabled        Status = "disabled"
	StatusChanged         Status = "changed"
)

// Statuses lists every state (the i18n catalog has one key per entry).
var Statuses = []Status{StatusAbsent, StatusRegistered, StatusLicenseRequired, StatusDetected, StatusDetectFailed,
	StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled, StatusChanged}

// Action is a user or system step.
type Action string

const (
	ActRegister      Action = "register"       // the user saved an absolute path (stat only, no exec)
	ActNeedLicense   Action = "need_license"   // a gated entry has no acceptance
	ActAcceptLicense Action = "accept_license" // license_required -> registered
	ActRevokeLicense Action = "revoke_license" // any registered state -> disabled
	ActDetect        Action = "detect"         // marks that Detect may run; the result is DetectOK/DetectFail
	ActDetectOK      Action = "detect_ok"
	ActDetectFail    Action = "detect_fail"
	ActTest          Action = "test"
	ActTestOK        Action = "test_ok"
	ActTestFail      Action = "test_fail"
	ActEnable        Action = "enable"
	ActDisable       Action = "disable"
	ActChanged       Action = "binary_changed" // health saw another sha256 or a missing file
)

// ErrTransition is returned for an action that is not allowed in a state.
type ErrTransition struct {
	From   Status
	Action Action
}

func (e *ErrTransition) Error() string {
	return fmt.Sprintf("extensions: action %q is not allowed in state %q", e.Action, e.From)
}

func in(s Status, set ...Status) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

// Next is the pure state machine. Detect and Test do not change the state themselves: the job reports DetectOK,
// DetectFail, TestOK or TestFail afterwards. ActDetect and ActTest only check that the step is allowed.
func Next(s Status, a Action) (Status, error) {
	deny := &ErrTransition{From: s, Action: a}
	registered := []Status{StatusRegistered, StatusDetected, StatusDetectFailed, StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled, StatusChanged}
	switch a {
	case ActRegister:
		// a new path may replace the old one in any state except a license that is still pending
		if in(s, StatusLicenseRequired) {
			return s, deny
		}
		return StatusRegistered, nil
	case ActNeedLicense:
		if s == StatusAbsent {
			return s, deny
		}
		return StatusLicenseRequired, nil
	case ActAcceptLicense:
		if s != StatusLicenseRequired {
			return s, deny
		}
		return StatusRegistered, nil
	case ActRevokeLicense:
		if !in(s, append(registered, StatusLicenseRequired)...) {
			return s, deny
		}
		return StatusDisabled, nil
	case ActDetect:
		// refused from license_required: only stat is allowed before acceptance
		if !in(s, registered...) {
			return s, deny
		}
		return s, nil
	case ActDetectOK:
		if !in(s, registered...) {
			return s, deny
		}
		return StatusDetected, nil
	case ActDetectFail:
		if !in(s, registered...) {
			return s, deny
		}
		return StatusDetectFailed, nil
	case ActTest:
		if !in(s, StatusDetected, StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled) {
			return s, deny
		}
		return s, nil
	case ActTestOK:
		if !in(s, StatusDetected, StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled) {
			return s, deny
		}
		if s == StatusEnabled {
			return s, nil
		}
		return StatusTested, nil
	case ActTestFail:
		if !in(s, StatusDetected, StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled) {
			return s, deny
		}
		return StatusTestFailed, nil
	case ActEnable:
		if !in(s, StatusTested, StatusDisabled) {
			return s, deny
		}
		return StatusEnabled, nil
	case ActDisable:
		if s != StatusEnabled {
			return s, deny
		}
		return StatusDisabled, nil
	case ActChanged:
		if !in(s, StatusDetected, StatusTested, StatusTestFailed, StatusEnabled, StatusDisabled) {
			return s, deny
		}
		return StatusChanged, nil
	}
	return s, deny
}

// CanEnable adds the manifest rule on top of Next: a detect-only entry only reports what is installed.
func CanEnable(m *Manifest, s Status) error {
	if m.Integration == DetectOnly {
		return fmt.Errorf("extensions: %s is detect-only and cannot be enabled", m.ID)
	}
	_, err := Next(s, ActEnable)
	return err
}
