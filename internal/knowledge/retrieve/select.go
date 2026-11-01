// Package retrieve ranks verified knowledge for a context packet (K21). It is pure: the server loads candidates from
// MongoDB (filtered by org and status) or from the Wiki index (re-resolved against MongoDB) and passes them in.
package retrieve

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"orqen/internal/contextpkt"
)

const (
	StrategyScopedText = "scoped_text"
	StrategyPinnedOnly = "pinned_only"
	StrategyOff        = "off"

	FreshStale = "evidence_stale"
)

type ClaimText struct {
	Text      string
	Supported bool
}

type Candidate struct {
	OrgID, RecordID, VersionID, ScopeKey string
	Experience                           bool
	Title, Body                          string
	Claims                               []ClaimText
	Score                                float64
	VerifiedAt                           time.Time
	ValidUntil                           *time.Time
	EvidenceStale                        bool
	Pinned, Critical                     bool
	AppliesTo                            []string
	ExternalEvidence, AuthorIsRole       bool
	UserAuthored                         bool
}

type In struct {
	Org            string
	Now            time.Time
	Chain          []string // scope keys in the run chain: org, project:<p>, sprint:<p>:<s>..., task:<run>
	Strategy       string
	TopK           int
	MaxSourceBytes int
	MaxAgeDays     int
	IncludeStale   bool
	Cands          []Candidate
}

// Out holds ready packet items. Excluded items carry a preset reason, so the manifest shows them.
type Out struct {
	Critical   []contextpkt.Item
	Knowledge  []contextpkt.Item
	Experience []contextpkt.Item
	// Violations counts candidates of another org that were dropped (event knowledge.scope_violation).
	Violations int
}

func scopeBonus(key string) float64 {
	switch {
	case strings.HasPrefix(key, "task:"):
		return 3
	case strings.HasPrefix(key, "sprint:"):
		return 2
	case strings.HasPrefix(key, "project:"):
		return 1
	}
	return 0
}

// RenderText renders a version for a packet: title, body (capped with a visible note) and the supported claims.
func RenderText(c Candidate, maxBytes int) string {
	var b strings.Builder
	b.WriteString("# " + c.Title + "\n")
	body := c.Body
	if maxBytes > 0 && len(body) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(body[cut]) {
			cut--
		}
		body = body[:cut] + fmt.Sprintf("\n[body truncated: showing %d of %d bytes]", cut, len(c.Body))
	}
	if body != "" {
		b.WriteString(body + "\n")
	}
	first := true
	for _, cl := range c.Claims {
		if !cl.Supported {
			continue
		}
		if first {
			b.WriteString("Claims:\n")
			first = false
		}
		b.WriteString("- " + cl.Text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (c Candidate) item(in In, kind string) contextpkt.Item {
	trust := "external"
	if c.UserAuthored && !c.ExternalEvidence && !c.AuthorIsRole {
		trust = "user"
	}
	fresh := ""
	if c.EvidenceStale {
		fresh = FreshStale
	}
	return contextpkt.Item{SourceID: c.VersionID, Kind: kind, Label: c.Title, Ref: "knowledge/" + c.RecordID,
		Text: RenderText(c, in.MaxSourceBytes), OrgID: c.OrgID, Trust: trust, RecordID: c.RecordID, VersionID: c.VersionID,
		Freshness: fresh, AppliesTo: c.AppliesTo}
}

func inChain(chain []string, key string) bool {
	for _, k := range chain {
		if k == key {
			return true
		}
	}
	return false
}

// Select applies K21: org and chain filter, expiry, staleness, ranking, per-section top_k. It is deterministic.
func Select(in In) Out {
	var out Out
	if in.Strategy == StrategyOff {
		return out
	}
	var live []Candidate
	for _, c := range in.Cands {
		if c.OrgID != in.Org {
			out.Violations++
			continue
		}
		if !inChain(in.Chain, c.ScopeKey) {
			continue
		}
		if in.Strategy == StrategyPinnedOnly && !c.Pinned {
			continue
		}
		live = append(live, c)
	}
	score := func(c Candidate) float64 {
		s := c.Score + scopeBonus(c.ScopeKey)
		if c.Pinned {
			s += 10
		}
		return s
	}
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i], live[j]
		if sa, sb := score(a), score(b); sa != sb {
			return sa > sb
		}
		if !a.VerifiedAt.Equal(b.VerifiedAt) {
			return a.VerifiedAt.After(b.VerifiedAt)
		}
		return a.VersionID < b.VersionID
	})
	topK := in.TopK
	if topK <= 0 {
		topK = 8
	}
	kept := map[string]int{}
	for _, c := range live {
		pinnedCritical := c.Pinned && c.Critical
		kind, section := "knowledge", "knowledge"
		if c.Experience {
			kind, section = "experience", "experience"
		}
		it := c.item(in, kind)
		switch {
		case c.ValidUntil != nil && c.ValidUntil.Before(in.Now):
			it.Excluded = contextpkt.ReasonExpired
		case !pinnedCritical && c.EvidenceStale && !in.IncludeStale:
			it.Excluded = contextpkt.ReasonStale
		case !pinnedCritical && in.MaxAgeDays > 0 && !c.VerifiedAt.IsZero() && c.VerifiedAt.Before(in.Now.AddDate(0, 0, -in.MaxAgeDays)):
			it.Excluded = contextpkt.ReasonStale
		}
		if pinnedCritical {
			// Stale evidence on a pinned critical item is flagged, never dropped.
			it.Critical = true
			if it.Excluded == "" || it.Excluded == contextpkt.ReasonStale {
				it.Excluded = ""
			}
			out.Critical = append(out.Critical, it)
			continue
		}
		if it.Excluded == "" {
			if kept[section] >= topK {
				it.Excluded = contextpkt.ReasonBelowRank
			} else {
				kept[section]++
			}
		}
		if c.Experience {
			out.Experience = append(out.Experience, it)
		} else {
			out.Knowledge = append(out.Knowledge, it)
		}
	}
	return out
}
