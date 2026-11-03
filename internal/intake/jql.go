// Package intake holds the pure Jira intake logic: JQL composition, polling windows, text normalization,
// acceptance and reference extraction, and the material fingerprints. It must not import internal/server.
package intake

import (
	"regexp"
	"strconv"
	"strings"
)

// JQLInput describes one poll. WindowMinutes 0 means a full scan (no updated clause).
type JQLInput struct {
	ProjectID             string
	UserJQL               *string
	BoardScrum            bool
	SprintIDs             []string
	RequireOpenSprint     bool
	IncludeUnresolvedDone bool
	Label                 string
	AssignedToMe          bool
	WindowMinutes         int
}

var orderByRe = regexp.MustCompile(`(?is)(^|\s+)ORDER\s+BY\s+.*$`)

// StripOrderBy removes a trailing ORDER BY clause. A string literal that contains "order by" is mangled; the
// mapping check still validates the JQL (documented risk).
func StripOrderBy(jql string) string { return strings.TrimSpace(orderByRe.ReplaceAllString(jql, "")) }

func quoteJQL(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

var digits = regexp.MustCompile(`^[0-9]+$`)

// ComposeJQL builds the eligibility query. The project clause is always first, so a user JQL cannot widen scope.
func ComposeJQL(in JQLInput) string {
	var b strings.Builder
	if digits.MatchString(in.ProjectID) {
		b.WriteString("project = " + in.ProjectID)
	} else {
		b.WriteString("project = " + quoteJQL(in.ProjectID))
	}
	user := ""
	if in.UserJQL != nil {
		user = StripOrderBy(*in.UserJQL)
	}
	switch {
	case user != "":
		b.WriteString(" AND (" + user + ")")
	case in.BoardScrum && in.RequireOpenSprint:
		ids := []string{}
		for _, id := range in.SprintIDs {
			if digits.MatchString(id) {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			b.WriteString(" AND sprint in (" + strings.Join(ids, ", ") + ")")
		} else {
			b.WriteString(" AND sprint in openSprints()")
		}
	}
	if in.Label != "" {
		b.WriteString(" AND labels = " + quoteJQL(in.Label))
	}
	if in.AssignedToMe {
		b.WriteString(" AND assignee = currentUser()")
	}
	if in.IncludeUnresolvedDone {
		b.WriteString(" AND (statusCategory != Done OR resolution = Unresolved)")
	} else {
		b.WriteString(" AND statusCategory != Done")
	}
	if in.WindowMinutes > 0 {
		b.WriteString(" AND updated >= -" + strconv.Itoa(in.WindowMinutes) + "m")
	}
	b.WriteString(" ORDER BY updated ASC, key ASC")
	return b.String()
}
