package skills

import "sort"

// Exclusion reasons.
const (
	ReasonBudget            = "skill_budget"
	ReasonNotApproved       = "not_approved"
	ReasonDisabledByProject = "disabled_by_project"
	ReasonNotFixRound       = "not_fix_round"
	ReasonNotForRole        = "not_for_role"
)

// Reasons lists every exclusion reason.
var Reasons = []string{ReasonBudget, ReasonNotApproved, ReasonDisabledByProject, ReasonNotFixRound, ReasonNotForRole}

// Binding attaches a skill version to a role in a scope.
type Binding struct {
	Role      string `json:"role" bson:"role"`
	SkillID   string `json:"skill_id" bson:"skill_id"`
	VersionID string `json:"version_id" bson:"version_id"`
	Enabled   bool   `json:"enabled" bson:"enabled"`
	Position  int    `json:"position" bson:"position"`
}

// VersionInfo is what resolution needs to know about a skill version.
type VersionInfo struct {
	SkillID   string
	VersionID string
	Slug      string
	Semver    string
	SHA256    string
	Body      string
	Roles     []string
	When      string
	Approved  bool
}

// Input of Resolve. Round 0 is the first attempt; fix rounds are > 0.
type Input struct {
	Org      []Binding
	Project  []Binding
	Role     string
	Round    int
	Versions map[string]VersionInfo // by version id
	Budget   int                    // skills.max_bytes_per_role; <= 0 means no skills fit
}

// Included is a resolved skill.
type Included struct {
	SkillID   string `json:"skill_id" bson:"skill_id"`
	VersionID string `json:"version_id" bson:"version_id"`
	Slug      string `json:"slug" bson:"slug"`
	Semver    string `json:"semver" bson:"semver"`
	SHA256    string `json:"sha256" bson:"sha256"`
	Bytes     int    `json:"bytes" bson:"bytes"`
}

// Excluded is a skill that was not used, with the reason.
type Excluded struct {
	Slug   string `json:"slug" bson:"slug"`
	Reason string `json:"reason" bson:"reason"`
}

// Manifest is the resolution result.
type Manifest struct {
	Included []Included `json:"included" bson:"included"`
	Excluded []Excluded `json:"excluded" bson:"excluded"`
	Bytes    int        `json:"bytes" bson:"bytes"`
	Budget   int        `json:"budget" bson:"budget"`
}

// Items returns the render items in order, looked up in versions.
func (m Manifest) Items(versions map[string]VersionInfo) []Item {
	out := make([]Item, 0, len(m.Included))
	for _, in := range m.Included {
		v := versions[in.VersionID]
		out = append(out, Item{Slug: in.Slug, Semver: in.Semver, SHA256: in.SHA256, Body: v.Body})
	}
	return out
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}

// Resolve picks the skills of one role for one round. A project entry for the same skill replaces the org entry,
// including enabled=false. Only approved versions count. Skills are ordered by position then slug and added until the
// next one no longer fits the budget; that one and every later one is excluded with skill_budget, never truncated.
func Resolve(in Input) Manifest {
	m := Manifest{Included: []Included{}, Excluded: []Excluded{}, Budget: in.Budget}
	type cand struct {
		b          Binding
		v          VersionInfo
		overridden bool
	}
	byskill := map[string]cand{}
	for _, b := range in.Org {
		if b.Role == in.Role {
			byskill[b.SkillID] = cand{b: b, v: in.Versions[b.VersionID]}
		}
	}
	for _, b := range in.Project {
		if b.Role == in.Role {
			_, had := byskill[b.SkillID]
			byskill[b.SkillID] = cand{b: b, v: in.Versions[b.VersionID], overridden: had}
		}
	}
	var live []cand
	for _, c := range byskill {
		name := c.v.Slug
		switch {
		case !c.b.Enabled:
			if c.overridden {
				m.Excluded = append(m.Excluded, Excluded{Slug: name, Reason: ReasonDisabledByProject})
			}
		case !c.v.Approved:
			m.Excluded = append(m.Excluded, Excluded{Slug: name, Reason: ReasonNotApproved})
		case !hasRole(c.v.Roles, in.Role):
			m.Excluded = append(m.Excluded, Excluded{Slug: name, Reason: ReasonNotForRole})
		case c.v.When == WhenFixRound && in.Round <= 0:
			m.Excluded = append(m.Excluded, Excluded{Slug: name, Reason: ReasonNotFixRound})
		default:
			live = append(live, c)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].b.Position != live[j].b.Position {
			return live[i].b.Position < live[j].b.Position
		}
		return live[i].v.Slug < live[j].v.Slug
	})
	used, full := 0, false
	for _, c := range live {
		sz := len(Section(Item{Slug: c.v.Slug, Semver: c.v.Semver, SHA256: c.v.SHA256, Body: c.v.Body}))
		need := sz
		if used == 0 {
			need += FrameBytes()
		} else {
			need++ // the blank line between sections
		}
		if full || used+need > in.Budget {
			full = true
			m.Excluded = append(m.Excluded, Excluded{Slug: c.v.Slug, Reason: ReasonBudget})
			continue
		}
		used += need
		m.Included = append(m.Included, Included{SkillID: c.v.SkillID, VersionID: c.v.VersionID, Slug: c.v.Slug, Semver: c.v.Semver, SHA256: c.v.SHA256, Bytes: sz})
	}
	m.Bytes = used // equals len(Render(items))
	sort.Slice(m.Excluded, func(i, j int) bool {
		if m.Excluded[i].Slug != m.Excluded[j].Slug {
			return m.Excluded[i].Slug < m.Excluded[j].Slug
		}
		return m.Excluded[i].Reason < m.Excluded[j].Reason
	})
	return m
}
