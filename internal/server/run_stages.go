package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/contextpkt"
	"orqen/internal/knowledge/retrieve"
	"orqen/internal/providers"
	"orqen/internal/runstate"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// contextDocs are the repository documents whose excerpts go into the packet.
var contextDocs = []string{"AGENTS.md", "CLAUDE.md", "README.md"}

// ContextFreshness records what the packet was built from.
type ContextFreshness struct {
	IssueSnapshotRevision int       `json:"issue_snapshot_revision"`
	IssueFetchedAt        string    `json:"issue_fetched_at"`
	RepoHeadSHA           string    `json:"repo_head_sha"`
	ContractSnapshotID    *string   `json:"contract_snapshot_id"`
	BuiltFrom             time.Time `json:"-"`
}

// contextInputs is everything the context stage reads outside a transaction.
type contextInputs struct {
	Project    ProjectDoc
	Snapshot   IssueSnapshotDoc
	Rules      []PinnedRuleDoc
	Contract   *ContractSnapshotDoc
	Docs       map[string]string // path -> text
	HeadSHA    string
	SprintGoal string
	Knowledge  *retrieve.Out
	Pages      map[string]contextpkt.Item // linked Confluence pages by URL
	MappedDocs []contextpkt.Item
	Graph      *contextpkt.Item // graph_neighbors, nil when the graph has nothing for the primary repo
	CodeImpact *contextpkt.Item // code_impact from the gitnexus index at the run's commit (role packets only)
}

func (a *App) loadRules(ctx context.Context, run RunDoc) ([]PinnedRuleDoc, error) {
	rules, err := findMany[PinnedRuleDoc](ctx, a.DB, "pinned_rules", bson.M{"org_id": run.OrgID, "active": true, "$or": []bson.M{{"scope": "org"}, {"scope": "project", "project_id": run.ProjectID}}}, bson.D{{Key: "scope", Value: 1}, {Key: "_id", Value: 1}}, 500)
	if err != nil {
		return nil, err
	}
	if a.ruleHook != nil {
		rules = a.ruleHook(rules)
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Scope == "org" && rules[j].Scope != "org" })
	return rules, nil
}

// readContext gathers the inputs of the packet; the only external calls are the repository HEAD and the sprint goal.
func (a *App) readContext(ctx context.Context, run RunDoc) (contextInputs, error) {
	in, err := a.readContextLocal(ctx, run)
	if err != nil {
		return in, err
	}
	return a.readContextRemote(ctx, run, in)
}

// readContextLocal gathers the stored inputs (project, issue snapshot, rules, repository documents) without any provider call.
func (a *App) readContextLocal(ctx context.Context, run RunDoc) (contextInputs, error) {
	var in contextInputs
	var err error
	if in.Project, err = a.project(ctx, run.OrgID, run.ProjectID); err != nil {
		return in, err
	}
	in.Graph = a.graphNeighbors(ctx, run, in.Project.Mapping.primary())
	if in.Snapshot, err = findOne[IssueSnapshotDoc](ctx, a.DB, "issue_snapshots", bson.M{"_id": run.SnapshotID, "org_id": run.OrgID}); err != nil {
		return in, err
	}
	if in.Rules, err = a.loadRules(ctx, run); err != nil {
		return in, err
	}
	in.Docs = map[string]string{}
	if id := in.Project.ReviewedContractSnapshotID; id != nil {
		c, e := a.contractSnapshot(ctx, run.OrgID, run.ProjectID, *id)
		if e != nil && !errors.Is(e, mongo.ErrNoDocuments) {
			return in, e
		}
		if e == nil {
			in.Contract = &c
			root := filepath.Join(a.Config.DataDir, "artifacts")
			for _, f := range c.Files {
				if !contains(contextDocs, path.Base(f.Path)) {
					continue
				}
				data, e := readBlob(root, run.OrgID, f.SHA256)
				if e != nil {
					continue // an unreadable document is left out, not fatal
				}
				in.Docs[f.Path] = string(data)
			}
		}
	}
	in.MappedDocs = a.mappedDocItems(ctx, run.OrgID, in.Project)
	return in, nil
}

func (a *App) readContextRemote(ctx context.Context, run RunDoc, in contextInputs) (contextInputs, error) {
	if in.Snapshot.Refs.Confluence != nil {
		in.Pages = a.linkedPages(ctx, run.OrgID, in.Project, in.Snapshot.Refs.Confluence)
	}
	if p := in.Project.Mapping.primary(); p != nil {
		conn, e := a.connection(ctx, run.OrgID, p.ConnectionID)
		if e == nil {
			var host providers.CodeHost
			if host, e = a.codeHost(ctx, conn); e == nil {
				if in.HeadSHA, e = host.ResolveCommit(ctx, p.ProviderProjectID, p.TargetBranch); e != nil {
					a.noteRateLimitErr(conn.ID, e)
					return in, e
				}
			} else {
				return in, e
			}
		} else if !errors.Is(e, mongo.ErrNoDocuments) {
			return in, e
		}
	}
	if j := in.Project.Mapping.Jira; j != nil && j.Board != nil {
		if conn, e := a.connection(ctx, run.OrgID, j.ConnectionID); e == nil {
			if src, e := a.issueSource(ctx, conn); e == nil {
				sprints, e := src.ActiveSprints(ctx, j.Board.ID)
				if pe, ok := providers.AsError(e); ok && pe.Kind == providers.KindUnsupported {
					e = nil
				}
				if e != nil {
					a.noteRateLimitErr(conn.ID, e)
					return in, e
				}
				var goals []string
				for _, s := range sprints {
					if s.Goal != "" {
						goals = append(goals, s.Name+": "+s.Goal)
					}
				}
				in.SprintGoal = strings.Join(goals, "\n")
			}
		}
	}
	return in, nil
}
func (a *App) noteRateLimitErr(conID string, err error) {
	if pe, ok := providers.AsError(err); ok {
		a.noteRateLimit(conID, pe)
	}
}

func commandLine(c CommandSpec) string {
	return fmt.Sprintf("%s (%s): %s", c.Key, c.Kind, strings.Join(c.Argv, " "))
}

// buildSections orders the packet sources (SPEC section 10).
func buildSections(run RunDoc, in contextInputs) []contextpkt.SectionIn {
	org := run.OrgID
	snap := in.Snapshot
	jira := func(id, kind, label, text string) contextpkt.Item {
		return contextpkt.Item{SourceID: id, Kind: kind, Label: label, Ref: snap.ID, Text: text, External: true, OrgID: org, ConnectionID: snap.ConnectionID}
	}
	issueSrc := ""
	for _, s := range run.Sources {
		if s.Kind == "jira_issue" {
			issueSrc = s.ID
			break
		}
	}
	// 1. critical rules (org first, then project).
	var critical, plain []contextpkt.Item
	for _, r := range in.Rules {
		it := contextpkt.Item{SourceID: r.ID, Kind: "pinned_rule", Label: r.Title, Ref: r.ID, Text: r.Body, Critical: r.Critical, AppliesTo: r.AppliesToRoles, OrgID: r.OrgID}
		if r.Scope == "project" && (r.ProjectID == nil || *r.ProjectID != run.ProjectID) {
			it.Excluded = contextpkt.ReasonOutOfScope
		}
		if r.Critical {
			critical = append(critical, it)
		} else {
			plain = append(plain, it)
		}
	}
	// 2. goal and acceptance.
	goal := []contextpkt.Item{}
	summary := jira(issueSrc+":summary", "jira_summary", run.IssueKey+" summary", snap.Summary)
	summary.Critical = true
	goal = append(goal, summary)
	if snap.Acceptance.Source != "none" && snap.Acceptance.Text != "" {
		acc := jira(issueSrc+":acceptance", "jira_acceptance", "acceptance ("+snap.Acceptance.Source+")", snap.Acceptance.Text)
		acc.Critical = true
		goal = append(goal, acc)
	}
	for _, s := range run.Sources {
		if !answerKind(s.Kind) || s.Text == nil {
			continue
		}
		if s.Carried && s.Code == "missing_acceptance" && snap.Acceptance.Source != "none" {
			continue // the issue has acceptance now; a carried answer no longer stands in for it
		}
		goal = append(goal, contextpkt.Item{SourceID: s.ID, Kind: s.Kind, Label: s.Label, Ref: s.Ref, Text: *s.Text, Critical: true, OrgID: org})
	}
	goal = append(goal, jira(issueSrc+":description", "jira_description", run.IssueKey+" description", snap.Description))
	if p := snap.Parent; p != nil {
		goal = append(goal, jira(issueSrc+":parent", "jira_parent", p.Key+" (parent)", p.Summary+"\n\n"+p.Description))
	}
	if in.SprintGoal != "" {
		goal = append(goal, jira(issueSrc+":sprint_goal", "jira_sprint", "sprint goal", in.SprintGoal))
	}
	// 3. repository rules: approved commands, protected paths, contract documents, the other pinned rules.
	repo := append([]contextpkt.Item{}, plain...)
	var cmds []string
	for _, c := range in.Project.Commands {
		if c.Approved {
			cmds = append(cmds, commandLine(c))
		}
	}
	if len(cmds) > 0 {
		repo = append(repo, contextpkt.Item{SourceID: "commands", Kind: "approved_commands", Label: "approved commands", Text: strings.Join(cmds, "\n"), OrgID: org})
	}
	var prot []string
	for _, p := range in.Project.ProtectedPaths {
		prot = append(prot, p.Mode+" "+p.Pattern)
	}
	if len(prot) > 0 {
		repo = append(repo, contextpkt.Item{SourceID: "protected_paths", Kind: "protected_paths", Label: "protected paths", Text: strings.Join(prot, "\n"), OrgID: org})
	}
	docNames := make([]string, 0, len(in.Docs))
	for p := range in.Docs {
		docNames = append(docNames, p)
	}
	sort.Strings(docNames)
	for _, p := range docNames {
		it := contextpkt.Item{SourceID: "doc:" + p, Kind: "repo_document", Label: p, Ref: *in.Project.ReviewedContractSnapshotID, Text: in.Docs[p], External: true, OrgID: org}
		if in.Contract != nil {
			it.ConnectionID = in.Contract.ConnectionID
		}
		if in.Project.LatestContractSnapshotID != nil && *in.Project.LatestContractSnapshotID != *in.Project.ReviewedContractSnapshotID {
			it.Excluded = contextpkt.ReasonStale
		}
		repo = append(repo, it)
	}
	// 4. design references: links only.
	design := []contextpkt.Item{}
	for _, u := range snap.Refs.Figma {
		design = append(design, contextpkt.Item{SourceID: "ref:" + u, Kind: "figma", Label: u, Ref: u, Excluded: contextpkt.ReasonAdapterUnavailable})
	}
	for _, u := range snap.Refs.Confluence {
		if it, ok := in.Pages[u]; ok {
			design = append(design, it)
			continue
		}
		design = append(design, contextpkt.Item{SourceID: "ref:" + u, Kind: "confluence", Label: u, Ref: u, Excluded: contextpkt.ReasonAdapterUnavailable})
	}
	design = append(design, in.MappedDocs...)
	// 5. code context.
	var code []contextpkt.Item
	if p := in.Project.Mapping.primary(); p != nil {
		text := fmt.Sprintf("repository: %s\ntarget branch: %s\nHEAD: %s", p.PathSnapshot, p.TargetBranch, in.HeadSHA)
		if p.PackagePath != nil {
			text += "\npackage path: " + *p.PackagePath
		}
		code = append(code, contextpkt.Item{SourceID: "repo_head", Kind: "repo_head", Label: "repository HEAD", Ref: in.HeadSHA, Text: text, OrgID: org})
	}
	if in.Graph != nil {
		code = append(code, *in.Graph)
	}
	if in.CodeImpact != nil {
		code = append(code, *in.CodeImpact)
	}
	// 6. task discussion: links, then comments newest first.
	var disc []contextpkt.Item
	if len(snap.Links) > 0 {
		var lines []string
		for _, l := range snap.Links {
			lines = append(lines, fmt.Sprintf("%s (%s) %s: %s", l.Type, l.Direction, l.Key, l.Summary))
		}
		disc = append(disc, jira(issueSrc+":links", "jira_links", "linked issues", strings.Join(lines, "\n")))
	}
	for i := len(snap.Comments) - 1; i >= 0; i-- {
		c := snap.Comments[i]
		disc = append(disc, jira(issueSrc+":comment:"+c.ID, "jira_comment", "comment by "+c.AuthorDisplay, c.Body))
	}
	knowledgeItems := []contextpkt.Item{}
	experienceItems := []contextpkt.Item{}
	if in.Knowledge != nil {
		critical = append(critical, in.Knowledge.Critical...)
		knowledgeItems, experienceItems = in.Knowledge.Knowledge, in.Knowledge.Experience
	}
	return []contextpkt.SectionIn{
		{ID: contextpkt.SecCriticalRules, Items: critical}, {ID: contextpkt.SecGoal, Items: goal}, {ID: contextpkt.SecRepoRules, Items: repo}, {ID: contextpkt.SecDesignRefs, Items: design},
		{ID: contextpkt.SecCode, Items: code}, {ID: contextpkt.SecDiscussion, Items: disc},
		{ID: contextpkt.SecKnowledge, Items: knowledgeItems},
		{ID: contextpkt.SecExperience, Items: experienceItems},
	}
}

// stageContext builds the packet and stores it as artifacts. An oversize critical set stops the run instead.
func (s *stageCtx) stageContext(ctx context.Context, run RunDoc) (bool, error) {
	a := s.a
	if run.State == runstate.Discovered {
		if err := a.fenced(ctx, s.lease, func(tx context.Context) error {
			_, e := a.casRun(tx, run, runstate.StageContext, bson.M{"state": runstate.GatheringContext}, runEvent{Reason: "advance", Actor: runstate.ActorSystem})
			return e
		}); err != nil {
			return false, err
		}
		var err error
		if run, err = a.run(ctx, run.OrgID, run.ID); err != nil {
			return false, err
		}
	}
	in, err := a.readContext(ctx, run)
	if err != nil {
		return false, err
	}
	allowed := []string{}
	for _, r := range in.Project.Mapping.Repos {
		allowed = append(allowed, r.ConnectionID)
	}
	if in.Project.Mapping.Jira != nil {
		allowed = append(allowed, in.Project.Mapping.Jira.ConnectionID)
	}
	for _, c := range in.Project.Mapping.Confluence {
		allowed = append(allowed, c.ConnectionID)
	}
	cap := run.Pinned.PacketCap
	kn, _, err := a.knowledgeFor(ctx, run, in.Snapshot.Summary)
	if err != nil {
		return false, err
	}
	in.Knowledge = &kn
	pkt, man, over := contextpkt.Build(contextpkt.In{Cap: cap, OrgID: run.OrgID, AllowedConnections: allowed, Sections: buildSections(run, in)})
	if over != nil {
		return false, &stageStop{Spec: stopSpec{Reason: "policy_conflict", QuestionCode: "packet_oversize", Params: bson.M{"critical_bytes": over.CriticalBytes, "cap": over.Cap},
			Options: []InboxOption{{ID: "raise_limit", EffectCode: "raise_context_limit"}, {ID: "cancel", EffectCode: "cancel_run"}}}}
	}
	var contractID *string
	if in.Contract != nil {
		contractID = &in.Contract.ID
	}
	fresh := ContextFreshness{IssueSnapshotRevision: in.Snapshot.Revision, IssueFetchedAt: timestamp(in.Snapshot.FetchedAt), RepoHeadSHA: in.HeadSHA, ContractSnapshotID: contractID}
	manifest := map[string]any{"version": man.Version, "cap": man.Cap, "bytes": man.Bytes, "critical_bytes": man.CriticalBytes, "sources_revision": run.SourcesRevision, "sections": man.Sections, "excluded": man.Excluded, "freshness": fresh}
	manJSON, err := json.Marshal(manifest)
	if err != nil {
		return false, err
	}
	root := filepath.Join(a.Config.DataDir, "artifacts")
	pktBlob, err := storeBlob(root, run.OrgID, strings.NewReader(pkt.Text), a.Config.ArtifactMaxBytes)
	if err != nil {
		return false, err
	}
	manBlob, err := storeBlob(root, run.OrgID, bytes.NewReader(manJSON), a.Config.ArtifactMaxBytes)
	if err != nil {
		return false, err
	}
	if err = a.hook(runstate.StageContext); err != nil {
		return false, err
	}
	err = a.fenced(ctx, s.lease, func(tx context.Context) error {
		pa, e := a.recordArtifact(tx, run.OrgID, pktBlob, "text/markdown", "context_packet", Subject{"run", run.ID})
		if e != nil {
			return e
		}
		ma, e := a.recordArtifact(tx, run.OrgID, manBlob, "application/json", "context_manifest", Subject{"run", run.ID})
		if e != nil {
			return e
		}
		built := RunContext{ArtifactID: pa.ID, ManifestArtifactID: ma.ID, Hash: pktBlob.SHA256, Bytes: pkt.Bytes, Cap: cap, SourcesRevision: run.SourcesRevision, BuiltAt: time.Now().UTC()}
		set := bson.M{"context": built}
		if in.HeadSHA != "" {
			set["repo_head_sha"] = in.HeadSHA
		}
		_, e = s.complete(tx, run, runstate.StageContext, runstate.StageTriage, set,
			eventSpec{"context.packet_built", bson.M{"run_id": run.ID, "bytes": pkt.Bytes, "cap": cap, "excluded": len(man.Excluded)}})
		return e
	})
	return false, err
}

// contextManifestDTO assembles ContextManifestDto from the stored manifest and the run.
func (a *App) contextManifestDTO(ctx context.Context, run RunDoc) (bson.M, error) {
	if run.Context == nil {
		return nil, problem("not_found")
	}
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": run.Context.ManifestArtifactID, "org_id": run.OrgID})
	if err != nil {
		return nil, err
	}
	data, err := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), run.OrgID, art.Sha256)
	if err != nil {
		return nil, err
	}
	var m bson.M
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["built_at"] = timestamp(run.Context.BuiltAt)
	m["packet_artifact_id"] = run.Context.ArtifactID
	return m, nil
}

// Stages implemented by later work packages park the run.
