package server

import (
	"context"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/readiness"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var acceptanceFieldRe = regexp.MustCompile(`^customfield_\d+$`)

type RepoMapping struct {
	Role                  string  `bson:"role" json:"role"`
	ConnectionID          string  `bson:"connection_id" json:"connection_id"`
	ProviderProjectID     string  `bson:"provider_project_id" json:"provider_project_id"`
	PathSnapshot          string  `bson:"path_snapshot" json:"path_snapshot"`
	DefaultBranchSnapshot *string `bson:"default_branch_snapshot" json:"default_branch_snapshot"`
	TargetBranch          string  `bson:"target_branch" json:"target_branch"`
	PackagePath           *string `bson:"package_path" json:"package_path"`
	Note                  *string `bson:"note" json:"note"`
}
type JiraBoardRef struct {
	ID           string  `bson:"id" json:"id"`
	NameSnapshot *string `bson:"name_snapshot" json:"name_snapshot"`
}
type JiraMapping struct {
	ConnectionID       string        `bson:"connection_id" json:"connection_id"`
	ProjectID          string        `bson:"project_id" json:"project_id"`
	ProjectKeySnapshot string        `bson:"project_key_snapshot" json:"project_key_snapshot"`
	Board              *JiraBoardRef `bson:"board" json:"board"`
	JQL                *string       `bson:"jql" json:"jql"`
	AcceptanceField    *string       `bson:"acceptance_field,omitempty" json:"acceptance_field"`
}
type ConfluenceRef struct {
	ConnectionID  string  `bson:"connection_id" json:"connection_id"`
	PageID        *string `bson:"page_id" json:"page_id"`
	SpaceKey      *string `bson:"space_key" json:"space_key"`
	TitleSnapshot *string `bson:"title_snapshot" json:"title_snapshot"`
}
type Mapping struct {
	Repos      []RepoMapping   `bson:"repos" json:"repos"`
	Jira       *JiraMapping    `bson:"jira" json:"jira"`
	Confluence []ConfluenceRef `bson:"confluence" json:"confluence"`
}
type CheckResult struct {
	Check  string  `bson:"check" json:"check"`
	Status string  `bson:"status" json:"status"`
	Code   *string `bson:"code" json:"code"`
	Detail string  `bson:"detail" json:"detail"`
}
type MappingCheck struct {
	MappingHash string        `bson:"mapping_hash" json:"mapping_hash"`
	CheckedAt   time.Time     `bson:"checked_at" json:"checked_at"`
	OperationID string        `bson:"operation_id" json:"operation_id"`
	Results     []CheckResult `bson:"results" json:"results"`
}
type ProfileBinding struct {
	ProfileID string    `bson:"profile_id" json:"profile_id"`
	VersionID string    `bson:"version_id" json:"version_id"`
	BoundAt   time.Time `bson:"bound_at" json:"bound_at"`
}

type ProjectDoc struct {
	ID                         string          `bson:"_id" json:"_id"`
	OrgID                      string          `bson:"org_id" json:"org_id"`
	Name                       string          `bson:"name" json:"name"`
	NameKey                    string          `bson:"name_key" json:"name_key"`
	Description                *string         `bson:"description" json:"description"`
	Mapping                    Mapping         `bson:"mapping" json:"mapping"`
	PrimaryRepoKey             *string         `bson:"primary_repo_key" json:"primary_repo_key"`
	MappingCheck               *MappingCheck   `bson:"mapping_check" json:"mapping_check"`
	Commands                   []CommandSpec   `bson:"commands" json:"commands"`
	ProtectedPaths             []ProtectedPath `bson:"protected_paths" json:"protected_paths"`
	LatestContractSnapshotID   *string         `bson:"latest_contract_snapshot_id" json:"latest_contract_snapshot_id"`
	ReviewedContractSnapshotID *string         `bson:"reviewed_contract_snapshot_id" json:"reviewed_contract_snapshot_id"`
	ProfileBinding             *ProfileBinding `bson:"profile_binding" json:"profile_binding"`
	AutomationEnabled          bool            `bson:"automation_enabled" json:"automation_enabled"`
	EnabledAt                  *time.Time      `bson:"enabled_at" json:"enabled_at"`
	EnabledPolicyVersionID     *string         `bson:"enabled_policy_version_id" json:"enabled_policy_version_id"`
	EnabledProfileVersionID    *string         `bson:"enabled_profile_version_id" json:"enabled_profile_version_id"`
	Active                     bool            `bson:"active" json:"active"`
	Revision                   int64           `bson:"revision" json:"revision"`
	CreatedAt                  time.Time       `bson:"created_at" json:"created_at"`
	UpdatedAt                  time.Time       `bson:"updated_at" json:"updated_at"`
	ArchivedAt                 *time.Time      `bson:"archived_at" json:"archived_at"`
}

func (m Mapping) normalized() Mapping {
	if m.Repos == nil {
		m.Repos = []RepoMapping{}
	}
	if m.Confluence == nil {
		m.Confluence = []ConfluenceRef{}
	}
	return m
}
func (m Mapping) primary() *RepoMapping {
	for i := range m.Repos {
		if m.Repos[i].Role == "primary" {
			return &m.Repos[i]
		}
	}
	return nil
}

// Hash covers everything a mapping check proves and none of the display snapshots or notes.
func (m Mapping) Hash() string {
	repos := []map[string]any{}
	for _, r := range m.Repos {
		repos = append(repos, map[string]any{"role": r.Role, "connection_id": r.ConnectionID, "provider_project_id": r.ProviderProjectID, "target_branch": r.TargetBranch, "package_path": r.PackagePath})
	}
	var jira any
	if j := m.Jira; j != nil {
		var board any
		if j.Board != nil {
			board = j.Board.ID
		}
		jm := map[string]any{"connection_id": j.ConnectionID, "project_id": j.ProjectID, "board_id": board, "jql": j.JQL}
		if j.AcceptanceField != nil { // included only when set, so earlier mapping checks stay valid
			jm["acceptance_field"] = *j.AcceptanceField
		}
		jira = jm
	}
	refs := []map[string]any{}
	for _, c := range m.Confluence {
		refs = append(refs, map[string]any{"connection_id": c.ConnectionID, "page_id": c.PageID, "space_key": c.SpaceKey})
	}
	return sha256Hex(canonicalJSON(map[string]any{"repos": repos, "jira": jira, "confluence": refs}))
}
func (d ProjectDoc) DTO() bson.M {
	state := readiness.StateDraft
	if d.AutomationEnabled {
		state = readiness.StateEnabled
	}
	commands, paths := d.Commands, d.ProtectedPaths
	if commands == nil {
		commands = []CommandSpec{}
	}
	if paths == nil {
		paths = []ProtectedPath{}
	}
	var check any
	if c := d.MappingCheck; c != nil {
		results := c.Results
		if results == nil {
			results = []CheckResult{}
		}
		check = bson.M{"mapping_hash": c.MappingHash, "checked_at": timestamp(c.CheckedAt), "operation_id": c.OperationID, "results": results}
	}
	var binding any
	if b := d.ProfileBinding; b != nil {
		binding = bson.M{"profile_id": b.ProfileID, "version_id": b.VersionID, "bound_at": timestamp(b.BoundAt)}
	}
	return bson.M{"id": d.ID, "name": d.Name, "description": d.Description, "mapping": d.Mapping.normalized(), "mapping_hash": d.Mapping.Hash(), "mapping_check": check, "commands": commands, "protected_paths": paths, "latest_contract_snapshot_id": d.LatestContractSnapshotID, "reviewed_contract_snapshot_id": d.ReviewedContractSnapshotID, "profile_binding": binding, "automation_enabled": d.AutomationEnabled, "enabled_at": optionalTime(d.EnabledAt), "enabled_policy_version_id": d.EnabledPolicyVersionID, "enabled_profile_version_id": d.EnabledProfileVersionID, "state": state, "blocker_count": 0, "revision": d.Revision, "created_at": timestamp(d.CreatedAt), "updated_at": timestamp(d.UpdatedAt)}
}

// SummaryDTO is the list/created shape; r carries the live readiness.
func (d ProjectDoc) SummaryDTO(r readiness.Result) bson.M {
	var primary any
	if p := d.Mapping.primary(); p != nil {
		primary = bson.M{"path": p.PathSnapshot, "connection_id": p.ConnectionID}
	}
	return bson.M{"id": d.ID, "name": d.Name, "primary_repo": primary, "state": r.State, "blocker_count": len(r.Blockers), "automation_enabled": d.AutomationEnabled, "revision": d.Revision}
}

type ProjectSummaryDoc struct{ ProjectDoc }

func (d ProjectSummaryDoc) DTO() bson.M {
	return d.SummaryDTO(readiness.Result{State: readiness.StateDraft})
}

type PatchProjectRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}
type FromCatalogRequest struct {
	ConnectionID       string   `json:"connection_id"`
	ProviderProjectIDs []string `json:"provider_project_ids"`
}
type RepoMappingInput struct {
	Role              string  `json:"role"`
	ConnectionID      string  `json:"connection_id"`
	ProviderProjectID string  `json:"provider_project_id"`
	TargetBranch      string  `json:"target_branch"`
	PackagePath       *string `json:"package_path"`
	Note              *string `json:"note"`
}
type JiraBoardInput struct {
	ID   string  `json:"id"`
	Name *string `json:"name"`
}
type JiraMappingInput struct {
	ConnectionID string          `json:"connection_id"`
	ProjectID    string          `json:"project_id"`
	ProjectKey   *string         `json:"project_key"`
	Board        *JiraBoardInput `json:"board"`
	JQL          *string         `json:"jql"`
	// AcceptanceField is an optional custom field id (customfield_NNNNN) holding the acceptance criteria.
	AcceptanceField *string `json:"acceptance_field"`
}
type ConfluenceRefInput struct {
	ConnectionID string  `json:"connection_id"`
	PageID       *string `json:"page_id"`
	SpaceKey     *string `json:"space_key"`
	Title        *string `json:"title"`
}
type MappingInput struct {
	Repos      []RepoMappingInput   `json:"repos"`
	Jira       *JiraMappingInput    `json:"jira"`
	Confluence []ConfluenceRefInput `json:"confluence"`
}

func prjFilter(org, id string) bson.M { return bson.M{"_id": id, "org_id": org, "active": true} }
func (a *App) project(ctx context.Context, org, id string) (ProjectDoc, error) {
	return findOne[ProjectDoc](ctx, a.DB, "projects", prjFilter(org, id))
}

// projectResponse writes a project with its live readiness and an ETag.
func (a *App) projectResponse(ctx context.Context, w http.ResponseWriter, status int, d ProjectDoc) error {
	res, err := a.readinessResults(ctx, d.OrgID, []ProjectDoc{d})
	if err != nil {
		return err
	}
	dto := d.DTO()
	dto["state"], dto["blocker_count"] = res[0].State, len(res[0].Blockers)
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, status, dto)
	return nil
}
func projectDuplicate(err error) error {
	if !mongo.IsDuplicateKeyError(err) {
		return err
	}
	switch {
	case strings.Contains(err.Error(), "org_primary_repo"):
		return &AppError{Code: "repo_already_mapped", Status: 422, Detail: "the repository is already mapped to a project", Errors: []FieldError{{"mapping.repos", "this repository is already mapped to another project"}}}
	case strings.Contains(err.Error(), "org_name_active"):
		return validation("name", "already used")
	}
	return err
}
func (a *App) listProjects(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	limit, after, err := pageQuery(r)
	if err != nil {
		return err
	}
	filter := bson.M{"org_id": org, "active": true}
	if search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search"))); search != "" {
		if len(search) > 200 {
			return validation("search", "must be at most 200 characters")
		}
		filter["name_key"] = bson.Regex{Pattern: regexp.QuoteMeta(search)}
	}
	docs, next, err := findPageBy(r.Context(), a.DB, "projects", filter, "_id", func(d ProjectDoc) string { return d.ID }, limit, after)
	if err != nil {
		return err
	}
	res, err := a.readinessResults(r.Context(), org, docs)
	if err != nil {
		return err
	}
	items := []bson.M{}
	for i, d := range docs {
		items = append(items, d.SummaryDTO(res[i]))
	}
	writeJSON(w, 200, bson.M{"items": items, "next_cursor": next})
	return nil
}
func (a *App) getProject(w http.ResponseWriter, r *http.Request) error {
	d, err := a.project(r.Context(), r.PathValue("orgId"), r.PathValue("prjId"))
	if err != nil {
		return err
	}
	return a.projectResponse(r.Context(), w, 200, d)
}
func (a *App) patchProject(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PatchProjectRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	set := bson.M{}
	if req.Name != nil {
		if !validName(*req.Name, 120) {
			return validation("name", "must be 1-120 characters")
		}
		set["name"] = strings.TrimSpace(*req.Name)
		set["name_key"] = strings.ToLower(strings.TrimSpace(*req.Name))
	}
	if req.Description != nil {
		d := strings.TrimSpace(*req.Description)
		if utf8.RuneCountInString(d) > 2000 {
			return validation("description", "must be at most 2000 characters")
		}
		if d == "" {
			set["description"] = nil
		} else {
			set["description"] = d
		}
	}
	if len(set) == 0 {
		return validation("body", "no changes given")
	}
	return a.updateProject(w, r, expected, "settings", func(context.Context, ProjectDoc) (bson.M, error) { return set, nil })
}

// updateProject applies a revision-checked change computed from the current document.
func (a *App) updateProject(w http.ResponseWriter, r *http.Request, expected int64, section string, change func(context.Context, ProjectDoc) (bson.M, error)) error {
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	var doc ProjectDoc
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		current, e := a.project(ctx, org, id)
		if e != nil {
			return e
		}
		set, e := change(ctx, current)
		if e != nil {
			return e
		}
		if doc, e = revisionUpdate[ProjectDoc](ctx, a.DB, "projects", prjFilter(org, id), expected, set); e != nil {
			return projectDuplicate(e)
		}
		return a.AppendEvent(ctx, org, "project.updated", Subject{"project", id}, bson.M{"name": doc.Name, "section": section}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	return a.projectResponse(r.Context(), w, 200, doc)
}
func (a *App) archiveProject(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		current, e := a.project(ctx, org, id)
		if e != nil {
			return e
		}
		if current.AutomationEnabled {
			return problem("automation_enabled")
		}
		doc, e := revisionUpdate[ProjectDoc](ctx, a.DB, "projects", prjFilter(org, id), expected, bson.M{"active": false, "archived_at": time.Now().UTC(), "primary_repo_key": nil})
		if e != nil {
			return e
		}
		return a.AppendEvent(ctx, org, "project.archived", Subject{"project", id}, bson.M{"name": doc.Name}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.WriteHeader(204)
	return nil
}
func (a *App) fromCatalog(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req FromCatalogRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/projects/from-catalog"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		if !validID("con", req.ConnectionID) {
			return Output{}, validation("connection_id", "is not a connection id")
		}
		if n := len(req.ProviderProjectIDs); n < 1 || n > 200 {
			return Output{}, validation("provider_project_ids", "must have 1 to 200 items")
		}
		conn, err := a.connection(ctx, org, req.ConnectionID)
		if err != nil {
			return Output{}, validation("connection_id", "unknown connection")
		}
		if conn.Provider != "gitlab" {
			return Output{}, providerMismatch()
		}
		created, existing, missing := []ProjectDoc{}, []bson.M{}, []string{}
		seen := map[string]bool{}
		keys := []string{}
		for _, pid := range req.ProviderProjectIDs {
			if pid == "" || len(pid) > 255 || seen[pid] {
				return Output{}, validation("provider_project_ids", "must be unique, non-empty ids")
			}
			seen[pid] = true
			keys = append(keys, repoKey(org, conn.ID, pid))
		}
		found, err := findMany[ProjectDoc](ctx, a.DB, "projects", bson.M{"org_id": org, "active": true, "primary_repo_key": bson.M{"$in": keys}}, bson.D{{Key: "_id", Value: 1}}, int64(len(keys)))
		if err != nil {
			return Output{}, err
		}
		byKey := map[string]string{}
		for _, p := range found {
			byKey[*p.PrimaryRepoKey] = p.ID
		}
		names := map[string]bool{}
		now := time.Now().UTC()
		for _, pid := range req.ProviderProjectIDs {
			if id, ok := byKey[repoKey(org, conn.ID, pid)]; ok {
				existing = append(existing, bson.M{"provider_project_id": pid, "project_id": id})
				continue
			}
			row, err := findOne[CatalogProjectDoc](ctx, a.DB, "catalog_projects", bson.M{"org_id": org, "connection_id": conn.ID, "provider_project_id": pid})
			if err == mongo.ErrNoDocuments || err == nil && row.Gone {
				missing = append(missing, pid)
				continue
			}
			if err != nil {
				return Output{}, err
			}
			name, err := a.uniqueProjectName(ctx, org, row.Name, names)
			if err != nil {
				return Output{}, err
			}
			branch := "main"
			if row.DefaultBranch != nil && validBranchName(*row.DefaultBranch) {
				branch = *row.DefaultBranch
			}
			pk := repoKey(org, conn.ID, pid)
			doc := ProjectDoc{ID: newID("prj"), OrgID: org, Name: name, NameKey: strings.ToLower(name), PrimaryRepoKey: &pk, Mapping: Mapping{Repos: []RepoMapping{{Role: "primary", ConnectionID: conn.ID, ProviderProjectID: pid, PathSnapshot: row.PathWithNamespace, DefaultBranchSnapshot: row.DefaultBranch, TargetBranch: branch}}, Confluence: []ConfluenceRef{}}, Commands: []CommandSpec{}, ProtectedPaths: []ProtectedPath{}, Active: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, err = a.DB.Coll("projects").InsertOne(ctx, doc); err != nil {
				if e := projectDuplicate(err); e != err {
					return Output{}, e
				}
				return Output{}, err
			}
			if err = a.AppendEvent(ctx, org, "project.created", Subject{"project", doc.ID}, bson.M{"name": doc.Name, "connection_id": conn.ID, "provider_project_id": pid}, &doc.Revision, nil, false); err != nil {
				return Output{}, err
			}
			created = append(created, doc)
		}
		res, err := a.readinessResults(ctx, org, created)
		if err != nil {
			return Output{}, err
		}
		summaries := []bson.M{}
		for i, d := range created {
			summaries = append(summaries, d.SummaryDTO(res[i]))
		}
		return Output{200, bson.M{"created": summaries, "existing": existing, "missing": missing}, nil}, nil
	})
}

// uniqueProjectName appends " (2)", " (3)" ... until no active project (or earlier project in this batch) uses the name.
func (a *App) uniqueProjectName(ctx context.Context, org, base string, batch map[string]bool) (string, error) {
	base = strings.TrimSpace(base)
	if utf8.RuneCountInString(base) > 120 {
		base = string([]rune(base)[:120])
	}
	for n := 1; n < 1000; n++ {
		name := base
		if n > 1 {
			suffix := fmt.Sprintf(" (%d)", n)
			trimmed := base
			if utf8.RuneCountInString(trimmed)+len(suffix) > 120 {
				trimmed = string([]rune(trimmed)[:120-len(suffix)])
			}
			name = trimmed + suffix
		}
		key := strings.ToLower(name)
		if batch[key] {
			continue
		}
		count, err := a.DB.Coll("projects").CountDocuments(ctx, bson.M{"org_id": org, "name_key": key, "active": true})
		if err != nil {
			return "", err
		}
		if count == 0 {
			batch[key] = true
			return name, nil
		}
	}
	return "", validation("name", "no free name")
}

// buildMapping validates a mapping request against the organization's connections and the refreshed catalog.
func (a *App) buildMapping(ctx context.Context, org string, in MappingInput, prev Mapping) (Mapping, *string, error) {
	var es []FieldError
	add := func(field, msg string) { es = append(es, FieldError{field, msg}) }
	conns := map[string]ConnectionDoc{}
	list, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil {
		return Mapping{}, nil, err
	}
	for _, c := range list {
		conns[c.ID] = c
	}
	var mismatch []FieldError
	connection := func(field, id, provider string) bool {
		c, ok := conns[id]
		switch {
		case !ok:
			add(field, "unknown connection")
		case c.Provider != provider:
			mismatch = append(mismatch, FieldError{field, "the connection is not a " + provider + " connection"})
		default:
			return true
		}
		return false
	}
	out := Mapping{Repos: []RepoMapping{}, Confluence: []ConfluenceRef{}}
	if len(in.Repos) > 20 {
		add("mapping.repos", "at most 20 repositories")
	}
	primaries := 0
	seen := map[string]bool{}
	var primaryKey *string
	for i, r := range in.Repos {
		f := func(n string) string { return fmt.Sprintf("mapping.repos[%d].%s", i, n) }
		if !contains([]string{"primary", "related"}, r.Role) {
			add(f("role"), "must be primary or related")
		}
		if r.Role == "primary" {
			primaries++
		}
		connection(f("connection_id"), r.ConnectionID, "gitlab")
		if r.ProviderProjectID == "" || len(r.ProviderProjectID) > 255 {
			add(f("provider_project_id"), "is required")
		}
		k := r.ConnectionID + "/" + r.ProviderProjectID
		if seen[k] {
			add(f("provider_project_id"), "the repository is listed twice")
		}
		seen[k] = true
		if !validBranchName(r.TargetBranch) {
			add(f("target_branch"), "must be a valid branch name")
		}
		var pkg *string
		if r.PackagePath != nil {
			p, ok := normalizeRelPath(*r.PackagePath)
			if !ok {
				add(f("package_path"), "must be a relative directory without ..")
			} else if p != "" {
				pkg = &p
			}
		}
		if r.Note != nil && utf8.RuneCountInString(*r.Note) > 500 {
			add(f("note"), "must be at most 500 characters")
		}
		repo := RepoMapping{Role: r.Role, ConnectionID: r.ConnectionID, ProviderProjectID: r.ProviderProjectID, PathSnapshot: r.ProviderProjectID, TargetBranch: r.TargetBranch, PackagePath: pkg, Note: r.Note}
		for _, old := range prev.Repos {
			if old.ConnectionID == r.ConnectionID && old.ProviderProjectID == r.ProviderProjectID {
				repo.PathSnapshot, repo.DefaultBranchSnapshot = old.PathSnapshot, old.DefaultBranchSnapshot
			}
		}
		row, err := findOne[CatalogProjectDoc](ctx, a.DB, "catalog_projects", bson.M{"org_id": org, "connection_id": r.ConnectionID, "provider_project_id": r.ProviderProjectID})
		if err == nil {
			repo.PathSnapshot, repo.DefaultBranchSnapshot = row.PathWithNamespace, row.DefaultBranch
		} else if err != mongo.ErrNoDocuments {
			return Mapping{}, nil, err
		}
		if r.Role == "primary" {
			pk := repoKey(org, r.ConnectionID, r.ProviderProjectID)
			primaryKey = &pk
		}
		out.Repos = append(out.Repos, repo)
	}
	if primaries > 1 {
		add("mapping.repos", "at most one primary repository")
	}
	if j := in.Jira; j != nil {
		connection("mapping.jira.connection_id", j.ConnectionID, "jira")
		if j.ProjectID == "" || len(j.ProjectID) > 255 {
			add("mapping.jira.project_id", "is required")
		}
		jm := &JiraMapping{ConnectionID: j.ConnectionID, ProjectID: j.ProjectID, ProjectKeySnapshot: j.ProjectID}
		if prev.Jira != nil && prev.Jira.ProjectID == j.ProjectID {
			jm.ProjectKeySnapshot = prev.Jira.ProjectKeySnapshot
		}
		if j.ProjectKey != nil && *j.ProjectKey != "" && len(*j.ProjectKey) <= 255 {
			jm.ProjectKeySnapshot = *j.ProjectKey
		}
		if b := j.Board; b != nil {
			if b.ID == "" || len(b.ID) > 255 {
				add("mapping.jira.board.id", "is required")
			}
			jm.Board = &JiraBoardRef{ID: b.ID, NameSnapshot: b.Name}
		}
		if j.JQL != nil {
			q := strings.TrimSpace(*j.JQL)
			if utf8.RuneCountInString(q) > 4000 {
				add("mapping.jira.jql", "must be at most 4000 characters")
			}
			if q != "" {
				jm.JQL = &q
			}
		}
		if j.AcceptanceField != nil && *j.AcceptanceField != "" {
			if !acceptanceFieldRe.MatchString(*j.AcceptanceField) {
				add("mapping.jira.acceptance_field", "must look like customfield_10100")
			} else {
				f := *j.AcceptanceField
				jm.AcceptanceField = &f
			}
		}
		out.Jira = jm
	}
	if len(in.Confluence) > 50 {
		add("mapping.confluence", "at most 50 references")
	}
	for i, c := range in.Confluence {
		f := func(n string) string { return fmt.Sprintf("mapping.confluence[%d].%s", i, n) }
		connection(f("connection_id"), c.ConnectionID, "confluence")
		if (c.PageID == nil || *c.PageID == "") && (c.SpaceKey == nil || *c.SpaceKey == "") {
			add(f("page_id"), "give a page id or a space key")
		}
		for name, v := range map[string]*string{"page_id": c.PageID, "space_key": c.SpaceKey, "title": c.Title} {
			if v != nil && len(*v) > 255 {
				add(f(name), "must be at most 255 characters")
			}
		}
		out.Confluence = append(out.Confluence, ConfluenceRef{ConnectionID: c.ConnectionID, PageID: c.PageID, SpaceKey: c.SpaceKey, TitleSnapshot: c.Title})
	}
	if len(mismatch) > 0 {
		return Mapping{}, nil, &AppError{Code: "provider_mismatch", Status: 422, Detail: "provider mismatch", Errors: mismatch}
	}
	if err := validationErrors(es); err != nil {
		return Mapping{}, nil, err
	}
	return out, primaryKey, nil
}
func (a *App) putMapping(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req MappingInput
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	return a.updateProject(w, r, expected, "mapping", func(ctx context.Context, cur ProjectDoc) (bson.M, error) {
		m, key, err := a.buildMapping(ctx, org, req, cur.Mapping)
		if err != nil {
			return nil, err
		}
		set := bson.M{"mapping": m, "primary_repo_key": key}
		if key == nil && cur.PrimaryRepoKey != nil || key != nil && (cur.PrimaryRepoKey == nil || *cur.PrimaryRepoKey != *key) {
			set["latest_contract_snapshot_id"], set["reviewed_contract_snapshot_id"] = nil, nil
		}
		return set, nil
	})
}
func (a *App) putCommands(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PutCommandsRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	return a.updateProject(w, r, expected, "commands", func(_ context.Context, cur ProjectDoc) (bson.M, error) {
		cwd := ""
		if p := cur.Mapping.primary(); p != nil && p.PackagePath != nil {
			cwd = *p.PackagePath
		}
		cmds, es := validateCommands(req.Commands, cur.Commands, cwd, time.Now().UTC())
		if err := validationErrors(es); err != nil {
			return nil, err
		}
		return bson.M{"commands": cmds}, nil
	})
}
func (a *App) putProtectedPaths(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatch(r)
	if err != nil {
		return err
	}
	var req PutProtectedPathsRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	rules, es := validateProtectedPaths(req.Rules)
	if err = validationErrors(es); err != nil {
		return err
	}
	return a.updateProject(w, r, expected, "protected_paths", func(context.Context, ProjectDoc) (bson.M, error) {
		return bson.M{"protected_paths": rules}, nil
	})
}
func (a *App) startMappingCheck(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org, id := r.PathValue("orgId"), r.PathValue("prjId")
	route := "POST /api/v1/orgs/{orgId}/projects/{prjId}/mapping/check"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org, "prjId": id}, nil), func(ctx context.Context) (Output, error) {
		if _, err := a.project(ctx, org, id); err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "mapping_check", Subject{"project", id}, "mapping_check:"+id, 3, bson.M{"project_id": id})
	})
}
func (a *App) getProjectReadiness(w http.ResponseWriter, r *http.Request) error {
	d, err := a.project(r.Context(), r.PathValue("orgId"), r.PathValue("prjId"))
	if err != nil {
		return err
	}
	res, err := a.readinessResults(r.Context(), d.OrgID, []ProjectDoc{d})
	if err != nil {
		return err
	}
	writeJSON(w, 200, readinessDTO(res[0]))
	return nil
}
func readinessDTO(r readiness.Result) bson.M {
	issues := func(in []readiness.Issue) []bson.M {
		out := []bson.M{}
		for _, i := range in {
			out = append(out, bson.M{"code": i.Code, "detail": i.Detail, "tab": i.Tab})
		}
		return out
	}
	return bson.M{"ready": r.Ready, "state": r.State, "blockers": issues(r.Blockers), "warnings": issues(r.Warnings), "evaluated_at": timestamp(time.Now().UTC())}
}
