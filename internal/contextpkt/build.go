package contextpkt

import "strings"

// Section ids in packet order (SPEC section 10).
const (
	SecCriticalRules = "critical_rules"
	SecGoal          = "goal_acceptance"
	SecRepoRules     = "repo_rules"
	SecDesignRefs    = "design_refs"
	SecCode          = "code_context"
	SecDiscussion    = "task_discussion"
	SecKnowledge     = "knowledge"
	SecExperience    = "experience"
)

// SectionOrder lists the eight sections.
var SectionOrder = []string{SecCriticalRules, SecGoal, SecRepoRules, SecDesignRefs, SecCode, SecDiscussion, SecKnowledge, SecExperience}

// Exclusion reasons.
const (
	ReasonBudget             = "budget"
	ReasonOutOfScope         = "out_of_scope"
	ReasonStale              = "stale"
	ReasonAdapterUnavailable = "adapter_unavailable"
	ReasonModuleUnavailable  = "module_unavailable"
	ReasonEmpty              = "empty"
	// Sprint 5.
	ReasonExpired     = "expired"
	ReasonBelowRank   = "below_rank"
	ReasonNotForRole  = "not_for_role"
	ReasonNotSynced   = "not_synced"
	ReasonNotVerified = "not_verified"
)

// SecEvidence is the extra section of the writer role packet (the knowledge evidence bundle).
const SecEvidence = "evidence"

// Item is one source. Critical items are never truncated and never dropped. External items are scrubbed of secrets;
// user-authored items (pinned rules, answers) are not.
type Item struct {
	SourceID, Kind, Label, Ref string
	Text                       string
	External                   bool
	Critical                   bool
	OrgID, ConnectionID        string
	// Excluded presets an exclusion reason (adapter_unavailable, module_unavailable, stale).
	Excluded string
	// Sprint 5. AppliesTo limits a critical item to roles (empty = all). Trust "external" overrides the default
	// (user) rendering for knowledge items; RecordID, VersionID and Freshness are shown in the manifest.
	AppliesTo                      []string
	Trust                          string
	RecordID, VersionID, Freshness string
}
type SectionIn struct {
	ID    string
	Items []Item
}

// In is the input of Build.
type In struct {
	Cap   int
	OrgID string
	// Role, when set, builds a per-role packet: a critical item that does not apply to the role is excluded with
	// reason not_for_role (visible, never silently dropped).
	Role string
	// AllowedConnections are the connections of the project mapping; an item naming another connection is out of scope.
	AllowedConnections []string
	Sections           []SectionIn
}
type Packet struct {
	Text  string
	Bytes int
}

// Oversize reports that the preamble and the critical items alone exceed the cap. No packet is produced.
type Oversize struct {
	CriticalBytes, Cap int
	Role               string
}
type ManifestItem struct {
	SourceID      string   `json:"source_id"`
	Kind          string   `json:"kind"`
	Label         string   `json:"label"`
	Ref           string   `json:"ref"`
	Bytes         int      `json:"bytes"`
	Included      bool     `json:"included"`
	Truncated     bool     `json:"truncated"`
	OriginalBytes int      `json:"original_bytes"`
	Reason        string   `json:"reason,omitempty"`
	Trust         string   `json:"trust,omitempty"`
	RecordID      string   `json:"record_id,omitempty"`
	VersionID     string   `json:"version_id,omitempty"`
	Freshness     string   `json:"freshness,omitempty"`
	AppliesTo     []string `json:"applies_to,omitempty"`
}
type ManifestSection struct {
	ID    string         `json:"id"`
	Bytes int            `json:"bytes"`
	Items []ManifestItem `json:"items"`
}
type Excluded struct {
	SourceID string `json:"source_id"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Reason   string `json:"reason"`
}
type Manifest struct {
	Version       int               `json:"version"`
	Cap           int               `json:"cap"`
	Bytes         int               `json:"bytes"`
	CriticalBytes int               `json:"critical_bytes"`
	Sections      []ManifestSection `json:"sections"`
	Excluded      []Excluded        `json:"excluded"`
	Role          string            `json:"role,omitempty"`
	Consumption   Consumption       `json:"consumption"`
}

type planned struct {
	sec, idx  int
	item      Item
	rendered  string
	original  int
	truncated bool
	include   bool
	reason    string
}

func inScope(in In, it Item) bool {
	if it.OrgID != "" && it.OrgID != in.OrgID {
		return false
	}
	if it.ConnectionID != "" {
		for _, c := range in.AllowedConnections {
			if c == it.ConnectionID {
				return true
			}
		}
		return false
	}
	return true
}

func (it Item) render(limit int) (out string, original int, truncated bool) {
	text := it.Text
	trust := "user"
	if it.External || it.Trust == "external" {
		text, trust = Scrub(text), "external"
	}
	original = len(text)
	if limit > 0 {
		text, truncated = truncate(text, limit)
	}
	return wrapFresh(it.SourceID, it.Kind, trust, it.Freshness, text), original, truncated
}

// Build assembles the packet. Critical items (and the preamble) must fit the cap; otherwise it returns an
// Oversize and no packet. Other items are capped at ItemLimit bytes each (recorded as truncated) and added in
// order while they fit; an item that does not fit is excluded with reason budget. Nothing is dropped silently.
func Build(in In) (Packet, Manifest, *Oversize) {
	man := Manifest{Version: 2, Role: in.Role, Cap: in.Cap, Sections: []ManifestSection{}, Excluded: []Excluded{}}
	for _, sec := range in.Sections {
		man.Sections = append(man.Sections, ManifestSection{ID: sec.ID, Items: []ManifestItem{}})
	}
	var plan []*planned
	for si, sec := range in.Sections {
		for ii, it := range sec.Items {
			p := &planned{sec: si, idx: ii, item: it}
			switch {
			case it.Excluded != "":
				p.reason = it.Excluded
			case !inScope(in, it):
				p.reason = ReasonOutOfScope
			case in.Role != "" && it.Critical && len(it.AppliesTo) > 0 && !contains(it.AppliesTo, in.Role):
				p.reason = ReasonNotForRole
			case strings.TrimSpace(it.Text) == "":
				p.reason = ReasonEmpty
			default:
				limit := ItemLimit
				if it.Critical {
					limit = 0
				}
				p.rendered, p.original, p.truncated = it.render(limit)
			}
			plan = append(plan, p)
		}
	}
	// Reserve preamble and critical items first.
	used := len(Preamble)
	headers := map[int]bool{}
	for _, p := range plan {
		if p.reason == "" && p.item.Critical {
			if !headers[p.sec] {
				headers[p.sec] = true
				used += len(sectionHeader(in.Sections[p.sec].ID))
			}
			used += len(p.rendered)
			p.include = true
		}
	}
	man.CriticalBytes = used
	if used > in.Cap {
		for _, p := range plan {
			man.addItem(in, p, p.reason == "" && p.item.Critical)
		}
		return Packet{}, man, &Oversize{CriticalBytes: used, Cap: in.Cap, Role: in.Role}
	}
	for _, p := range plan {
		if p.reason != "" || p.item.Critical {
			continue
		}
		extra := len(p.rendered)
		if !headers[p.sec] {
			extra += len(sectionHeader(in.Sections[p.sec].ID))
		}
		if used+extra > in.Cap {
			p.reason = ReasonBudget
			continue
		}
		used += extra
		headers[p.sec] = true
		p.include = true
	}
	var b strings.Builder
	b.WriteString(Preamble)
	for si, sec := range in.Sections {
		started := false
		for _, p := range plan {
			if p.sec != si || !p.include {
				continue
			}
			if !started {
				b.WriteString(sectionHeader(sec.ID))
				started = true
			}
			b.WriteString(p.rendered)
		}
	}
	for _, p := range plan {
		man.addItem(in, p, p.include)
	}
	for i := range man.Sections {
		for _, it := range man.Sections[i].Items {
			if it.Included {
				man.Sections[i].Bytes += it.Bytes
			}
		}
	}
	text := b.String()
	man.Bytes = len(text)
	man.Consumption = NewConsumption(man.Bytes, in.Cap, man.CriticalBytes)
	return Packet{Text: text, Bytes: len(text)}, man, nil
}

func (m *Manifest) addItem(_ In, p *planned, included bool) {
	it := p.item
	trust := "user"
	if it.External || it.Trust == "external" {
		trust = "external"
	}
	mi := ManifestItem{SourceID: it.SourceID, Kind: it.Kind, Label: it.Label, Ref: it.Ref, Included: included, Truncated: included && p.truncated, OriginalBytes: p.original, Reason: p.reason,
		Trust: trust, RecordID: it.RecordID, VersionID: it.VersionID, Freshness: it.Freshness, AppliesTo: it.AppliesTo}
	if included {
		mi.Bytes = len(p.rendered)
	} else {
		mi.Reason = p.reason
		if mi.Reason == "" {
			mi.Reason = ReasonBudget
		}
		m.Excluded = append(m.Excluded, Excluded{SourceID: it.SourceID, Kind: it.Kind, Label: it.Label, Reason: mi.Reason})
	}
	m.Sections[p.sec].Items = append(m.Sections[p.sec].Items, mi)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
