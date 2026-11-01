package backup

import (
	"sort"
	"time"
)

// Candidate is a backup directory considered for pruning.
type Candidate struct {
	Name      string
	CreatedAt time.Time
	Valid     bool
}

// PrunePlan is the result of PlanPrune.
type PrunePlan struct {
	Delete  []string `json:"delete"`
	Keep    []string `json:"keep"`
	Ignored []string `json:"ignored"` // foreign names and backups whose manifest does not validate: never touched
}

// PlanPrune decides which backups to delete. Only names that match NamePattern and whose manifest validates are
// candidates; everything else is ignored. The newest keepLast always survive. Of the rest, a backup is deleted when
// keepDays is 0 (no age rule: count alone decides) or when it is older than keepDays days.
func PlanPrune(entries []Candidate, keepLast, keepDays int, now time.Time) PrunePlan {
	plan := PrunePlan{Delete: []string{}, Keep: []string{}, Ignored: []string{}}
	var ok []Candidate
	for _, e := range entries {
		if !NamePattern.MatchString(e.Name) || !e.Valid {
			plan.Ignored = append(plan.Ignored, e.Name)
			continue
		}
		ok = append(ok, e)
	}
	sort.Slice(ok, func(i, j int) bool {
		if !ok[i].CreatedAt.Equal(ok[j].CreatedAt) {
			return ok[i].CreatedAt.After(ok[j].CreatedAt)
		}
		return ok[i].Name > ok[j].Name
	})
	if keepLast < 1 {
		keepLast = 1
	}
	for i, e := range ok {
		switch {
		case i < keepLast:
			plan.Keep = append(plan.Keep, e.Name)
		case keepDays == 0 || now.Sub(e.CreatedAt) > time.Duration(keepDays)*24*time.Hour:
			plan.Delete = append(plan.Delete, e.Name)
		default:
			plan.Keep = append(plan.Keep, e.Name)
		}
	}
	sort.Strings(plan.Ignored)
	return plan
}

// Due reports whether a scheduled backup is due: backups are enabled and the last successful one is older than the interval.
func Due(enabled bool, lastSuccess *time.Time, intervalHours int, now time.Time) bool {
	if !enabled {
		return false
	}
	if lastSuccess == nil {
		return true
	}
	return now.Sub(*lastSuccess) >= time.Duration(intervalHours)*time.Hour
}
