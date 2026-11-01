package backup

import "sort"

// BatchSize is the insert batch of a restore.
const BatchSize = 500

// Lock states of restore_locks.
const (
	LockInProgress = "in_progress"
	LockDone       = "done"
	LockFailed     = "failed"
)

// Refusal codes.
const (
	CodeTargetNotEmpty = "restore_target_not_empty"
	CodeServerRunning  = "restore_server_running"
	CodeIncomplete     = "restored_incomplete"
)

// Target describes the install a restore would write into.
type Target struct {
	Counts     map[string]int64 // documents per included collection
	EventsOnly bool             // counters holds only _id:"events" with seq 0
	Blobs      int              // files below <data>/artifacts
}

// NonEmpty lists why a target is not empty: the included collections that hold documents (counters excepted when it
// is the pristine events counter) and "artifacts_dir" when blobs exist. An empty result means the target is empty.
func (t Target) NonEmpty() []string {
	var out []string
	for name, n := range t.Counts {
		if n == 0 || !IsIncluded(name) {
			continue
		}
		if name == "counters" && t.EventsOnly {
			continue
		}
		out = append(out, name)
	}
	if t.Blobs > 0 {
		out = append(out, "artifacts_dir")
	}
	sort.Strings(out)
	return out
}

// Empty reports whether the target can receive a restore.
func (t Target) Empty() bool { return len(t.NonEmpty()) == 0 }

// ReplaceOnRestore lists the collections whose documents may already exist in an empty install and are replaced by id
// rather than inserted: the counters (the events counter is created by migration).
var ReplaceOnRestore = map[string]bool{"counters": true}
