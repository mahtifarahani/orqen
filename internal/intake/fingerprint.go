package intake

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"orqen/internal/canon"
)

// OrqenMarker is written into comments Orqen itself posts (Sprint 4); such comments never count as material.
const OrqenMarker = "orqen:run="

type ParentRef struct{ ID, Summary, Description string }
type LinkRef struct{ Type, Direction, ID, Summary, StatusCategory string }
type AttachmentRef struct {
	ID, Filename, Mime string
	Size               int64
}
type CommentRef struct{ ID, Body string }

// Snapshot is the normalized issue content the fingerprints are computed over.
type Snapshot struct {
	Summary, Description string
	Acceptance           Acceptance
	IssueType            string
	Components, Labels   []string
	Parent               *ParentRef
	Links                []LinkRef
	Attachments          []AttachmentRef
	Comments             []CommentRef
	// Volatile fields: part of ContentHash only.
	StatusName, StatusCategory, Priority, AssigneeID, Resolution, Updated string
}

func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
func sha(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

type fpLink struct {
	Type      string `json:"type"`
	Direction string `json:"direction"`
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	Status    string `json:"status,omitempty"`
}

func links(in []LinkRef, withStatus bool) []fpLink {
	out := make([]fpLink, 0, len(in))
	for _, l := range in {
		x := fpLink{l.Type, l.Direction, l.ID, Normalize(l.Summary), ""}
		if withStatus {
			x.Status = l.StatusCategory
		}
		out = append(out, x)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Type != out[b].Type {
			return out[a].Type < out[b].Type
		}
		if out[a].Direction != out[b].Direction {
			return out[a].Direction < out[b].Direction
		}
		return out[a].ID < out[b].ID
	})
	return out
}

type fpAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Mime     string `json:"mime"`
}

func attachments(in []AttachmentRef) []fpAttachment {
	out := make([]fpAttachment, 0, len(in))
	for _, a := range in {
		out = append(out, fpAttachment{a.ID, a.Filename, a.Size, a.Mime})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

type fpComment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

func comments(in []CommentRef, dropMarker bool) []fpComment {
	out := make([]fpComment, 0, len(in))
	for _, c := range in {
		if dropMarker && strings.Contains(c.Body, OrqenMarker) {
			continue
		}
		out = append(out, fpComment{c.ID, Normalize(c.Body)})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}
func parent(p *ParentRef) any {
	if p == nil {
		return nil
	}
	return map[string]string{"id": p.ID, "summary": Normalize(p.Summary), "description": Normalize(p.Description)}
}

// MaterialFP is the fingerprint of what can change the work: summary, description, acceptance, type, components,
// labels (without the intake label), parent, links, attachments and non-Orqen comments. Status, resolution,
// priority, assignee, updated, rank, sprint and link status are not material.
func MaterialFP(s Snapshot, label string) string {
	labels := []string{}
	for _, l := range s.Labels {
		if l != label {
			labels = append(labels, l)
		}
	}
	return sha(canon.JSON(map[string]any{
		"summary": Normalize(s.Summary), "description": Normalize(s.Description),
		"acceptance": map[string]string{"text": Normalize(s.Acceptance.Text), "source": s.Acceptance.Source},
		"issue_type": s.IssueType, "components": sortedCopy(s.Components), "labels": sortedCopy(labels),
		"parent": parent(s.Parent), "links": links(s.Links, false), "attachments": attachments(s.Attachments), "comments": comments(s.Comments, true),
	}))
}

// ContentHash covers every stored field, including the volatile ones; it decides whether a new snapshot row is stored.
func ContentHash(s Snapshot) string {
	return sha(canon.JSON(map[string]any{
		"summary": Normalize(s.Summary), "description": Normalize(s.Description),
		"acceptance": map[string]string{"text": Normalize(s.Acceptance.Text), "source": s.Acceptance.Source},
		"issue_type": s.IssueType, "components": sortedCopy(s.Components), "labels": sortedCopy(s.Labels),
		"parent": parent(s.Parent), "links": links(s.Links, true), "attachments": attachments(s.Attachments), "comments": comments(s.Comments, false),
		"status": s.StatusName, "status_category": s.StatusCategory, "priority": s.Priority, "assignee": s.AssigneeID, "resolution": s.Resolution, "updated": s.Updated,
	}))
}

// PolicyIntent identifies what the run would do for an issue in a project.
func PolicyIntent(prj, repoKey, branch string) string {
	return sha("jira_to_mr\n" + prj + "\n" + repoKey + "\n" + branch)
}

// IntentKey makes a run unique per issue content and policy intent.
func IntentKey(org, con, issue, fp, policyIntent string) string {
	return sha(org + "\n" + con + "\n" + issue + "\n" + fp + "\n" + policyIntent)
}
