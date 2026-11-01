package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/contextpkt"
	"orqen/internal/knowledge/retrieve"
	"orqen/internal/rolebriefs"
)

// runChain lists the scope keys whose knowledge may enter a run's packets (K21).
func runChain(run RunDoc) []string {
	return []string{"org", "project:" + run.ProjectID, "task:" + run.ID}
}

// knowledgeFor loads the verified candidates of the run chain from MongoDB and ranks them with the pure selector.
// The strategy and limits come from the policy pinned at run creation. degraded names why retrieval is off.
func (a *App) knowledgeFor(ctx context.Context, run RunDoc, query string) (retrieve.Out, string, error) {
	pv, err := a.pinnedPolicy(ctx, run)
	if err != nil {
		return retrieve.Out{}, "", err
	}
	strategy := pv.Str("knowledge.retrieval.strategy")
	if strategy == "" {
		strategy = retrieve.StrategyScopedText
	}
	chain := runChain(run)
	vs, err := findMany[KnowledgeVersionDoc](ctx, a.DB, "knowledge_versions", bson.M{"org_id": run.OrgID, "status": "verified", "scope_key": bson.M{"$in": chain}}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return retrieve.Out{}, "", err
	}
	scores := map[string]float64{}
	if strategy == retrieve.StrategyScopedText && strings.TrimSpace(query) != "" {
		if hits, e := a.mongoSearch(ctx, run.OrgID, query, "", "", 50); e == nil {
			for _, h := range hits {
				scores[h.VersionID] = h.Score
			}
		}
	}
	cands := make([]retrieve.Candidate, 0, len(vs))
	for _, v := range vs {
		rec, e := a.knowledgeRecord(ctx, run.OrgID, v.RecordID)
		if e != nil || !rec.Active {
			continue
		}
		c := retrieve.Candidate{OrgID: v.OrgID, RecordID: v.RecordID, VersionID: v.ID, ScopeKey: v.ScopeKey, Experience: v.Type == "experience", Title: v.Title, Body: v.Body,
			Score: scores[v.ID], ValidUntil: v.ValidUntil, EvidenceStale: v.EvidenceStale, Pinned: rec.Pinned, Critical: rec.Critical, AppliesTo: rec.AppliesToRoles,
			AuthorIsRole: v.Origin.Kind == "role", UserAuthored: v.Promotion != nil && v.Promotion.Mode == "user"}
		if v.VerifiedAt != nil {
			c.VerifiedAt = *v.VerifiedAt
		}
		for _, cl := range v.Claims {
			c.Claims = append(c.Claims, retrieve.ClaimText{Text: cl.Text, Supported: cl.Verdict == "supported"})
			for _, ev := range cl.Evidence {
				if ev.RefKind == "source_version" {
					c.ExternalEvidence = true
				}
			}
		}
		cands = append(cands, c)
	}
	out := retrieve.Select(retrieve.In{Org: run.OrgID, Now: time.Now().UTC(), Chain: chain, Strategy: strategy, TopK: pv.Int("knowledge.retrieval.top_k"),
		MaxSourceBytes: pv.Int("knowledge.retrieval.max_source_bytes"), MaxAgeDays: pv.Int("knowledge.retrieval.max_age_days"), IncludeStale: pv.Bool("knowledge.retrieval.include_stale"), Cands: cands})
	degraded := ""
	if strategy == retrieve.StrategyOff {
		degraded = "retrieval_off"
	} else if a.indexFor(ctx, run.OrgID) == nil {
		degraded = "wiki_not_configured"
	}
	if out.Violations > 0 {
		_ = a.AppendEvent(ctx, run.OrgID, "knowledge.scope_violation", Subject{"run", run.ID}, bson.M{"dropped": out.Violations}, nil, nil, true)
		a.Committed()
	}
	return out, degraded, nil
}

// rolePacketData builds the packet of one role at dispatch time from stored sources (no provider calls).
func (a *App) rolePacket(ctx context.Context, run RunDoc, role string) (contextpkt.Packet, contextpkt.Manifest, *contextpkt.Oversize, error) {
	in, err := a.readContextLocal(ctx, run)
	if err != nil {
		return contextpkt.Packet{}, contextpkt.Manifest{}, nil, err
	}
	if run.RepoHeadSHA != nil {
		in.HeadSHA = *run.RepoHeadSHA
	}
	kn, _, err := a.knowledgeFor(ctx, run, in.Snapshot.Summary)
	if err != nil {
		return contextpkt.Packet{}, contextpkt.Manifest{}, nil, err
	}
	in.Knowledge = &kn
	if pv, perr := a.pinnedPolicy(ctx, run); perr == nil {
		in.CodeImpact = a.codeImpactItem(ctx, run, role, in.HeadSHA, pv)
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
	pkt, man, over := contextpkt.BuildRole(contextpkt.In{Cap: run.Pinned.PacketCap, OrgID: run.OrgID, AllowedConnections: allowed, Sections: buildSections(run, in)}, role)
	return pkt, man, over, nil
}

// attachContext puts the role packet into the brief data. A critical set that does not fit stops the run with
// packet_oversize{role}; nothing is truncated.
func (a *App) attachContext(ctx context.Context, run RunDoc, role string, d *rolebriefs.Data) error {
	pkt, man, over, err := a.rolePacket(ctx, run, role)
	if err != nil {
		return err
	}
	if over != nil {
		return &stageStop{Spec: stopSpec{Reason: "policy_conflict", QuestionCode: "packet_oversize", Params: bson.M{"critical_bytes": over.CriticalBytes, "cap": over.Cap, "role": role},
			Options: []InboxOption{{ID: "raise_limit", EffectCode: "raise_context_limit"}, {ID: "cancel", EffectCode: "cancel_run"}}}}
	}
	d.Context = pkt.Text
	// Approved skills pinned at run creation; the only place the brief data gets its skills block.
	block, _, err := a.skillsFor(ctx, run, role, run.Round)
	if err != nil {
		return err
	}
	d.Skills = block
	a.recordUsage(ctx, run, role, man)
	return nil
}

// recordUsage notes which knowledge versions reached which role (one row per run, role and round).
func (a *App) recordUsage(ctx context.Context, run RunDoc, role string, man contextpkt.Manifest) {
	ids := []string{}
	for _, s := range man.Sections {
		for _, it := range s.Items {
			if it.VersionID != "" {
				ids = append(ids, it.VersionID)
			}
		}
	}
	id := usageID(run, role)
	_, _ = a.DB.Coll("knowledge_usage").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"org_id": run.OrgID, "run_id": run.ID, "role": role, "round": run.Round, "version_ids": ids,
		"packet_bytes": man.Bytes, "est_tokens": man.Consumption.EstTokens, "est_method": man.Consumption.EstMethod, "actual_input_tokens": nil, "at": time.Now().UTC()}}, upsertOpt())
}

// getRoleContext builds a role packet manifest on demand for the Context tab.
func (a *App) getRoleContext(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	run, err := a.run(r.Context(), org, r.PathValue("runId"))
	if err != nil {
		return err
	}
	role := r.PathValue("role")
	if _, ok := contextpkt.RoleProfiles[role]; !ok {
		return validation("role", "unknown role")
	}
	_, man, over, err := a.rolePacket(r.Context(), run, role)
	if err != nil {
		return err
	}
	body := bson.M{"role": role, "manifest": man, "oversize": nil}
	if over != nil {
		body["oversize"] = bson.M{"critical_bytes": over.CriticalBytes, "cap": over.Cap, "role": role}
	}
	writeJSON(w, 200, body)
	return nil
}
