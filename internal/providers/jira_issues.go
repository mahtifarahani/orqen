package providers

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// jiraStatus, jiraNamed and jiraUser mirror the JSON shapes shared by search and issue reads.
type jiraStatus struct {
	Name           string `json:"name"`
	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}
type jiraNamed struct {
	Name string `json:"name"`
}
type jiraUser struct {
	AccountID   string `json:"accountId"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
}

func (u *jiraUser) id() string {
	if u == nil {
		return ""
	}
	return first(u.AccountID, u.Key, u.Name)
}

type jiraHeadFields struct {
	Summary   string      `json:"summary"`
	Status    *jiraStatus `json:"status"`
	Labels    []string    `json:"labels"`
	Assignee  *jiraUser   `json:"assignee"`
	IssueType *jiraNamed  `json:"issuetype"`
	Priority  *jiraNamed  `json:"priority"`
	Updated   string      `json:"updated"`
}

func (f jiraHeadFields) head(id, key string) IssueHead {
	h := IssueHead{ID: id, Key: key, Summary: f.Summary, Labels: append([]string{}, f.Labels...), Updated: f.Updated, AssigneeID: f.Assignee.id()}
	if f.Status != nil {
		h.StatusName, h.StatusCategory = f.Status.Name, f.Status.StatusCategory.Key
	}
	if f.IssueType != nil {
		h.IssueType = f.IssueType.Name
	}
	if f.Priority != nil {
		h.Priority = f.Priority.Name
	}
	return h
}

// SearchIssues: Cloud uses /rest/api/3/search/jql with a nextPageToken cursor, Server and Data Center use
// /rest/api/2/search with startAt (Next is then the next startAt as a string).
func (j *jira) SearchIssues(ctx context.Context, jql string, fields []string, page string, limit int) (IssuePage, error) {
	limit = min(max(limit, 1), 100)
	q := url.Values{"jql": {jql}, "maxResults": {strconv.Itoa(limit)}}
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	type issue struct {
		ID     flex           `json:"id"`
		Key    string         `json:"key"`
		Fields jiraHeadFields `json:"fields"`
	}
	conv := func(in []issue) []IssueHead {
		out := make([]IssueHead, 0, len(in))
		for _, i := range in {
			out = append(out, i.Fields.head(string(i.ID), i.Key))
		}
		return out
	}
	if j.cloud() {
		if page != "" {
			q.Set("nextPageToken", page)
		}
		var r struct {
			Issues        []issue `json:"issues"`
			NextPageToken string  `json:"nextPageToken"`
			IsLast        bool    `json:"isLast"`
		}
		if _, err := j.getJSON(ctx, j.u("/rest/api/3/search/jql", q), &r); err != nil {
			return IssuePage{}, jqlError(err)
		}
		out := IssuePage{Items: conv(r.Issues)}
		if !r.IsLast && len(r.Issues) > 0 {
			out.Next = r.NextPageToken
		}
		return out, nil
	}
	start := 0
	if page != "" {
		n, err := strconv.Atoi(page)
		if err != nil || n < 0 {
			return IssuePage{}, newError(KindInvalidOutput, "invalid_cursor", "the page cursor is invalid")
		}
		start = n
	}
	q.Set("startAt", strconv.Itoa(start))
	var r struct {
		Issues []issue `json:"issues"`
		Total  int     `json:"total"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/search", q), &r); err != nil {
		return IssuePage{}, jqlError(err)
	}
	out := IssuePage{Items: conv(r.Issues)}
	if next := start + len(r.Issues); len(r.Issues) > 0 && next < r.Total {
		out.Next = strconv.Itoa(next)
	}
	return out, nil
}
func jqlError(err error) error {
	if e, ok := AsError(err); ok && e.Kind == KindInvalidRequest {
		e.Code = "jql_invalid"
	}
	return err
}

type jiraIssueRaw struct {
	ID     flex                       `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// GetIssue reads one issue through the v2 endpoint, so description and comment bodies are strings. An ADF object
// (some Cloud tenants) is flattened to text.
func (j *jira) GetIssue(ctx context.Context, id string, fields []string) (Issue, error) {
	q := url.Values{}
	if len(fields) > 0 {
		q.Set("fields", strings.Join(fields, ","))
	}
	var raw jiraIssueRaw
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/issue/"+escape(id), q), &raw); err != nil {
		return Issue{}, err
	}
	return parseIssue(raw)
}
func parseIssue(raw jiraIssueRaw) (Issue, error) {
	var head jiraHeadFields
	if b, err := json.Marshal(raw.Fields); err == nil {
		_ = json.Unmarshal(b, &head)
	}
	if head.Labels == nil {
		head.Labels = []string{}
	}
	is := Issue{IssueHead: head.head(string(raw.ID), raw.Key), Fields: map[string]json.RawMessage{}, Components: []string{}, Links: []Link{}, Attachments: []Attachment{}}
	known := map[string]bool{"summary": true, "status": true, "labels": true, "assignee": true, "issuetype": true, "priority": true, "updated": true, "description": true, "components": true, "resolution": true, "parent": true, "issuelinks": true, "attachment": true}
	for k, v := range raw.Fields {
		if !known[k] {
			is.Fields[k] = v
		}
	}
	if d, ok := raw.Fields["description"]; ok {
		is.Description, is.DescriptionADF = TextOf(d)
	}
	var comps []jiraNamed
	if json.Unmarshal(raw.Fields["components"], &comps) == nil {
		for _, c := range comps {
			is.Components = append(is.Components, c.Name)
		}
	}
	var res *jiraNamed
	if json.Unmarshal(raw.Fields["resolution"], &res) == nil && res != nil {
		is.Resolution = res.Name
	}
	var parent *struct {
		ID     flex   `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary string `json:"summary"`
		} `json:"fields"`
	}
	if json.Unmarshal(raw.Fields["parent"], &parent) == nil && parent != nil {
		is.Parent = &IssueRef{ID: string(parent.ID), Key: parent.Key, Summary: parent.Fields.Summary}
	}
	type linked struct {
		ID     flex   `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary string      `json:"summary"`
			Status  *jiraStatus `json:"status"`
		} `json:"fields"`
	}
	var links []struct {
		Type         jiraNamed `json:"type"`
		InwardIssue  *linked   `json:"inwardIssue"`
		OutwardIssue *linked   `json:"outwardIssue"`
	}
	if json.Unmarshal(raw.Fields["issuelinks"], &links) == nil {
		for _, l := range links {
			add := func(dir string, x *linked) {
				if x == nil {
					return
				}
				link := Link{Type: l.Type.Name, Direction: dir, ID: string(x.ID), Key: x.Key, Summary: x.Fields.Summary}
				if x.Fields.Status != nil {
					link.StatusCategory = x.Fields.Status.StatusCategory.Key
				}
				is.Links = append(is.Links, link)
			}
			add("inward", l.InwardIssue)
			add("outward", l.OutwardIssue)
		}
	}
	var atts []struct {
		ID       flex   `json:"id"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		Mime     string `json:"mimeType"`
		Created  string `json:"created"`
	}
	if json.Unmarshal(raw.Fields["attachment"], &atts) == nil {
		for _, a := range atts {
			is.Attachments = append(is.Attachments, Attachment{ID: string(a.ID), Filename: a.Filename, Size: a.Size, Mime: a.Mime, Created: a.Created})
		}
	}
	return is, nil
}

// ListComments reads one page of comments (oldest first).
func (j *jira) ListComments(ctx context.Context, id string, startAt, limit int) (CommentsPage, error) {
	q := url.Values{"startAt": {strconv.Itoa(startAt)}, "maxResults": {strconv.Itoa(min(max(limit, 1), 100))}, "orderBy": {"created"}}
	var r struct {
		Comments []struct {
			ID      flex            `json:"id"`
			Author  *jiraUser       `json:"author"`
			Created string          `json:"created"`
			Updated string          `json:"updated"`
			Body    json.RawMessage `json:"body"`
		} `json:"comments"`
		Total int `json:"total"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/api/2/issue/"+escape(id)+"/comment", q), &r); err != nil {
		return CommentsPage{}, err
	}
	page := CommentsPage{Items: make([]Comment, 0, len(r.Comments)), NextStartAt: -1, Total: r.Total}
	for _, c := range r.Comments {
		body, _ := TextOf(c.Body)
		display := ""
		if c.Author != nil {
			display = c.Author.DisplayName
		}
		page.Items = append(page.Items, Comment{ID: string(c.ID), AuthorID: c.Author.id(), AuthorDisplay: display, Created: c.Created, Updated: c.Updated, Body: body})
	}
	if next := startAt + len(r.Comments); len(r.Comments) > 0 && next < r.Total {
		page.NextStartAt = next
	}
	return page, nil
}

// ActiveSprints lists the active sprints of a board.
func (j *jira) ActiveSprints(ctx context.Context, boardID string) ([]Sprint, error) {
	var r struct {
		Values []struct {
			ID   flex   `json:"id"`
			Name string `json:"name"`
			Goal string `json:"goal"`
		} `json:"values"`
	}
	if _, err := j.getJSON(ctx, j.u("/rest/agile/1.0/board/"+escape(boardID)+"/sprint", url.Values{"state": {"active"}}), &r); err != nil {
		return nil, agile404(err)
	}
	out := make([]Sprint, 0, len(r.Values))
	for _, s := range r.Values {
		out = append(out, Sprint{ID: string(s.ID), Name: s.Name, Goal: s.Goal})
	}
	return out, nil
}

// TextOf renders a Jira rich-text value as text. A JSON string is returned as is (adf=false); an ADF document
// contributes its text nodes depth-first, with a newline between blocks (adf=true).
func TextOf(raw json.RawMessage) (text string, adf bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var node any
	if json.Unmarshal(raw, &node) != nil {
		return "", false
	}
	var b strings.Builder
	var walk func(n any)
	walk = func(n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		typ, _ := m["type"].(string)
		if typ == "text" {
			t, _ := m["text"].(string)
			b.WriteString(t)
			return
		}
		if typ == "hardBreak" {
			b.WriteString("\n")
			return
		}
		kids, _ := m["content"].([]any)
		block := typ == "paragraph" || typ == "heading" || typ == "listItem" || typ == "codeBlock" || typ == "blockquote" || typ == "bulletList" || typ == "orderedList" || typ == "rule"
		if typ == "listItem" {
			b.WriteString("- ")
		}
		for _, k := range kids {
			walk(k)
		}
		if block && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
	}
	walk(node)
	return strings.TrimRight(b.String(), "\n"), true
}
