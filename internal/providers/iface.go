package providers

import (
	"context"
	"encoding/json"
)

type Identity struct {
	ProviderUserID, Username, DisplayName string
}

const (
	FeatureAvailable   = "available"
	FeatureDenied      = "denied"
	FeatureUnsupported = "unsupported"
	FeatureUnknown     = "unknown"
)

type Capabilities struct {
	ServerVersion, Deployment string
	TokenScopes               []string // nil: unknown
	Features                  map[string]string
}
type Project struct {
	ID, Name, PathWithNamespace, DefaultBranch, WebURL, Visibility, LastActivityAt string
	Archived                                                                       bool
	// HTTPURLToRepo is the https clone URL the host reports (it must not carry credentials).
	HTTPURLToRepo string
}

// ProjectsPage.Next is an opaque cursor, empty on the last page.
type ProjectsPage struct {
	Items []Project
	Next  string
}
type Branch struct {
	Name               string
	Default, Protected bool
}
type TreeEntry struct {
	Name, Path, Type string
}
type JiraProject struct{ ID, Key, Name string }
type JiraProjectsPage struct {
	Items       []JiraProject
	NextStartAt int // -1 on the last page
	Truncated   bool
}
type Board struct {
	ID, Name, Type, ProjectID string
}
type BoardsPage struct {
	Items       []Board
	NextStartAt int // -1 on the last page
}

type CodeHost interface {
	Identity(ctx context.Context) (Identity, error)
	Capabilities(ctx context.Context) (Capabilities, error)
	ListProjectsPage(ctx context.Context, cursor string) (ProjectsPage, error)
	GetProject(ctx context.Context, id string) (Project, error)
	ListBranches(ctx context.Context, pid, search string, limit int) ([]Branch, error)
	GetBranch(ctx context.Context, pid, name string) (Branch, error)
	ListTree(ctx context.Context, pid, ref, path string) ([]TreeEntry, error)
	ReadFileRaw(ctx context.Context, pid, ref, path string, max int64) ([]byte, bool, error)
	ResolveCommit(ctx context.Context, pid, ref string) (string, error)
	// Merge request and pipeline reads (Sprint 4). Everything here is GET.
	GetMR(ctx context.Context, pid string, iid int) (MergeRequest, error)
	ListMRPipelines(ctx context.Context, pid string, iid int) ([]PipelineRef, error)
	GetPipeline(ctx context.Context, pid string, id int) (PipelineRef, error)
	ListPipelineJobs(ctx context.Context, pid string, id int) ([]PipelineJob, error)
	ListMRDiscussionsUnresolved(ctx context.Context, pid string, iid int) (int, error)
}

// MergeRequest is the read model of a merge request.
type MergeRequest struct {
	IID                         int
	State                       string
	SHA                         string
	WebURL                      string
	Title                       string
	Draft                       bool
	HasConflicts                bool
	BlockingDiscussionsResolved bool
	BaseSHA, StartSHA, HeadSHA  string
	MergeCommitSHA              string
	Description                 string
}

// PipelineRef is a pipeline of a commit.
type PipelineRef struct {
	ID                  int
	SHA, Status, Source string
}

// PipelineJob is one job of a pipeline.
type PipelineJob struct {
	Name, Status string
	AllowFailure bool
}
type IssueSource interface {
	Identity(ctx context.Context) (Identity, error)
	Capabilities(ctx context.Context) (Capabilities, error)
	ListProjects(ctx context.Context, query string, startAt, limit int) (JiraProjectsPage, error)
	GetProject(ctx context.Context, id string) (JiraProject, error)
	ListBoards(ctx context.Context, projectID string, startAt, limit int) (BoardsPage, error)
	GetBoard(ctx context.Context, id string) (Board, error)
	CheckJQL(ctx context.Context, jql string) error
	// SearchIssues runs a JQL search. page is the opaque cursor returned in IssuePage.Next ("" for the first page).
	SearchIssues(ctx context.Context, jql string, fields []string, page string, limit int) (IssuePage, error)
	GetIssue(ctx context.Context, id string, fields []string) (Issue, error)
	ListComments(ctx context.Context, id string, startAt, limit int) (CommentsPage, error)
	// ActiveSprints returns the active sprints of a board; an unavailable Agile API is KindUnsupported.
	ActiveSprints(ctx context.Context, boardID string) ([]Sprint, error)
}

// IssueHead is the minimal projection a poll reads.
type IssueHead struct {
	ID, Key, Updated, Summary, StatusName, StatusCategory string
	Labels                                                []string
	AssigneeID, Priority, IssueType                       string
}
type IssuePage struct {
	Items []IssueHead
	Next  string // opaque cursor, empty on the last page
}
type IssueRef struct{ ID, Key, Summary string }
type Link struct{ Type, Direction, ID, Key, Summary, StatusCategory string }
type Attachment struct {
	ID, Filename, Mime, Created string
	Size                        int64
}

// Issue is the full read used by ingestion. Description is plain text even when Jira sent ADF.
type Issue struct {
	IssueHead
	Description    string
	DescriptionADF bool
	// Fields holds the extra requested fields (for example the acceptance custom field), raw.
	Fields      map[string]json.RawMessage
	Components  []string
	Resolution  string
	Parent      *IssueRef
	Links       []Link
	Attachments []Attachment
}
type Comment struct{ ID, AuthorID, AuthorDisplay, Created, Updated, Body string }
type CommentsPage struct {
	Items       []Comment
	NextStartAt int // -1 on the last page
	Total       int
}
type Sprint struct{ ID, Name, Goal string }

// DocsSource reads wiki pages (Confluence). Everything here is GET; there is no write method.
type DocsSource interface {
	Identity(ctx context.Context) (Identity, error)
	Capabilities(ctx context.Context) (Capabilities, error)
	GetPage(ctx context.Context, id string) (Page, error)
	GetPageVersion(ctx context.Context, id string, version int) (Page, error)
	Search(ctx context.Context, cql string, limit int) ([]PageRef, error)
}

// PageRef is a search hit.
type PageRef struct{ ID, Title, SpaceKey, URL string }

// Page is one page version. Text is the converted plain text (capped at the adapter's page cap, Truncated says so);
// Storage is the raw storage representation, capped the same way. ContentHash is sha256 of the S11-normalized text,
// RawHash of the raw storage.
type Page struct {
	ID, Title, SpaceKey, URL string
	Version                  int
	UpdatedAt                string
	Storage, Text            string
	Links                    []string
	ContentHash, RawHash     string
	Truncated                bool
}
