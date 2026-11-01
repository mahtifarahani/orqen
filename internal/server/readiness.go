package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"orqen/internal/catherd"
	"orqen/internal/readiness"
	"time"
)

// readinessShared is loaded once per evaluation and shared by every project of the organization.
type readinessShared struct {
	conns    map[string]ConnectionDoc
	catalog  map[string]CatalogProjectDoc
	versions map[string]ModelProfileVersionDoc
	models   *catherd.Catalog
	state    *CatherdStateDoc
	host     string // the organization's effective orchestration host
	secrets  map[string]CatherdSecretDoc
	hostDoc  *HostStateDoc
	xws      ExternalWriteSettingsDoc
	wcreds   map[string]PublisherCredDoc
}

// readinessResults evaluates the gate for projects of one organization, loading shared inputs once.
func (a *App) readinessResults(ctx context.Context, org string, docs []ProjectDoc) ([]readiness.Result, error) {
	out := make([]readiness.Result, len(docs))
	if len(docs) == 0 {
		return out, nil
	}
	list, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil {
		return nil, err
	}
	conns := map[string]ConnectionDoc{}
	for _, c := range list {
		conns[c.ID] = c
	}
	var or []bson.M
	for _, d := range docs {
		if p := d.Mapping.primary(); p != nil {
			or = append(or, bson.M{"connection_id": p.ConnectionID, "provider_project_id": p.ProviderProjectID})
		}
	}
	catalog := map[string]CatalogProjectDoc{}
	if len(or) > 0 {
		rows, err := findMany[CatalogProjectDoc](ctx, a.DB, "catalog_projects", bson.M{"org_id": org, "$or": or}, bson.D{{Key: "_id", Value: 1}}, int64(len(or)))
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			catalog[repoKey(org, row.ConnectionID, row.ProviderProjectID)] = row
		}
	}
	sh := readinessShared{conns: conns, catalog: catalog, versions: map[string]ModelProfileVersionDoc{}}
	var ids []string
	for _, d := range docs {
		if d.ProfileBinding != nil {
			ids = append(ids, d.ProfileBinding.VersionID)
		}
	}
	if len(ids) > 0 {
		vs, err := findMany[ModelProfileVersionDoc](ctx, a.DB, "model_profile_versions", bson.M{"org_id": org, "_id": bson.M{"$in": ids}}, bson.D{{Key: "_id", Value: 1}}, int64(len(ids)))
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			sh.versions[v.ID] = v
		}
		if sh.models, err = a.currentCatalog(ctx, org); err != nil {
			return nil, err
		}
	}
	if sh.state, err = a.catherdState(ctx, org); err != nil {
		return nil, err
	}
	if sh.host, err = a.orgHostE(ctx, org); err != nil {
		return nil, err
	}
	if sh.secrets, err = a.secretDocs(ctx, org); err != nil {
		return nil, err
	}
	if hd, e := findOne[HostStateDoc](ctx, a.DB, "host_states", bson.M{"_id": hostStateID(org), "org_id": org}); e == nil {
		sh.hostDoc = &hd
	}
	if sh.xws, err = a.externalWriteSettings(ctx, org); err != nil {
		return nil, err
	}
	sh.wcreds = map[string]PublisherCredDoc{}
	if pcs, e := findMany[PublisherCredDoc](ctx, a.DB, "publisher_credentials", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 200); e == nil {
		for _, pc := range pcs {
			sh.wcreds[pc.ConnectionID] = pc
		}
	}
	for i, d := range docs {
		out[i] = readiness.Evaluate(a.readinessInputs(d, sh))
	}
	return out, nil
}

func readinessSecret(d CatherdSecretDoc) readiness.Secret {
	return readiness.Secret{Set: d.isSet(), Status: d.checkStatus()}
}

// usedBackends lists the backends of every rung a profile document can run, failover stand-ins included.
func usedBackends(doc map[string]any) []string {
	seen := map[string]bool{}
	add := func(rung string) {
		if r, ok := catherd.ParseRung(rung); ok {
			seen[r.Backend] = true
		}
	}
	for _, role := range catherd.Roles {
		for _, r := range catherd.RungsOf(doc, role) {
			add(r)
		}
	}
	if fo, ok := doc["failover"].(map[string]any); ok {
		for k, v := range fo {
			add(k)
			if s, ok := v.(string); ok {
				add(s)
			}
		}
	}
	out := []string{}
	for b := range seen {
		out = append(out, b)
	}
	return out
}
func profileInput(v ModelProfileVersionDoc, sh readinessShared) *readiness.Profile {
	doc := plain(v.Doc)
	budget, _ := doc["budget"].(map[string]any)
	p := &readiness.Profile{Published: v.State == "published", ReviewerEnabled: catherd.RoleEnabled(doc, "reviewer"), VerifierEnabled: catherd.RoleEnabled(doc, "verifier"),
		Independence: catherd.ComputeIndependence(doc, sh.models).Status, BudgetChosen: len(budget) > 0 || v.Meta.BudgetUncappedAck, UsedBackends: usedBackends(doc), BoundHost: v.hostOrDefault(), IsolatedBackends: isolatedKeyed(doc)}
	if j, ok := doc["jev"].(map[string]any); ok {
		p.JevAuto = j["use"] == "auto"
	}
	if sh.state != nil && sh.state.Inventory != nil {
		for _, n := range sh.state.Inventory.Names {
			p.InCatherd = p.InCatherd || n == v.CatherdName
		}
	}
	return p
}
func connectionInput(c ConnectionDoc, ok bool) readiness.Connection {
	if !ok {
		return readiness.Connection{Reasons: []string{"connection_missing"}}
	}
	reasons := c.readinessReasons()
	return readiness.Connection{Ready: len(reasons) == 0, Reasons: reasons}
}
func (a *App) readinessInputs(d ProjectDoc, sh readinessShared) readiness.Inputs {
	ctx := context.Background()
	conns, catalog := sh.conns, sh.catalog
	in := readiness.Inputs{Enabled: d.AutomationEnabled, MappingHash: d.Mapping.Hash()}
	if p := d.Mapping.primary(); p != nil {
		in.HasPrimaryRepo, in.TargetBranch = true, p.TargetBranch
		c, ok := conns[p.ConnectionID]
		in.RepoConnection = connectionInput(c, ok)
		if row, found := catalog[repoKey(d.OrgID, p.ConnectionID, p.ProviderProjectID)]; found {
			in.Catalog = readiness.CatalogRow{Present: true, Gone: row.Gone, Archived: row.Archived}
		}
	}
	if j := d.Mapping.Jira; j != nil {
		in.HasJira = true
		c, ok := conns[j.ConnectionID]
		in.JiraConnection = connectionInput(c, ok)
		in.JiraHasBoard = j.Board != nil
		in.JiraHasJQL = j.JQL != nil && *j.JQL != ""
	}
	if c := d.MappingCheck; c != nil {
		mc := &readiness.MappingCheck{Hash: c.MappingHash}
		for _, r := range c.Results {
			if r.Status == "failed" {
				mc.Failed = append(mc.Failed, r.Check)
			}
		}
		in.Check = mc
	}
	in.ConfluenceRefs = len(d.Mapping.Confluence)
	for _, ref := range d.Mapping.Confluence {
		if ref.PageID == nil {
			continue
		}
		if n, _ := a.DB.Coll("source_versions").CountDocuments(ctx, bson.M{"org_id": d.OrgID, "kind": "confluence_page", "connection_id": ref.ConnectionID, "external_id": *ref.PageID}); n == 0 {
			in.ConfluenceUnsynced++
		}
	}
	if st, err := findOne[SourceSyncStateDoc](ctx, a.DB, "source_sync_states", bson.M{"_id": d.ID, "org_id": d.OrgID}); err == nil {
		for _, ref := range d.Mapping.Confluence {
			if ref.PageID == nil {
				continue
			}
			for _, r := range st.Refs {
				if r.ConnectionID == ref.ConnectionID && r.PageID == *ref.PageID {
					if r.OK {
						in.ConfluenceVerified++
					} else {
						in.ConfluenceSyncFailed++
					}
				}
			}
		}
	}
	in.KnowledgeIndex = a.knowledgeIndexFact(ctx, d.OrgID)
	if in.ConfluenceRefs > 0 {
		n, _ := a.DB.Coll("knowledge_versions").CountDocuments(ctx, bson.M{"org_id": d.OrgID, "status": "verified", "evidence_stale": true})
		in.DocsStale = int(n)
	}
	for _, c := range d.Commands {
		in.Commands = append(in.Commands, readiness.Command{Key: c.Key, Kind: c.Kind, Approved: c.Approved})
	}
	if d.LatestContractSnapshotID != nil {
		in.LatestContract = *d.LatestContractSnapshotID
	}
	if d.ReviewedContractSnapshotID != nil {
		in.ReviewedContract = *d.ReviewedContractSnapshotID
	}
	if b := d.ProfileBinding; b != nil {
		if v, ok := sh.versions[b.VersionID]; ok {
			in.Profile = profileInput(v, sh)
		} else {
			in.Profile = &readiness.Profile{Independence: "unknown", BoundHost: sh.host} // the bound version vanished: not published
		}
	}
	if rt := a.CatherdRT; rt != nil {
		in.CatherdAvailable, in.CatherdSupported = rt.Available(), rt.Supported()
	}
	in.EffectiveHost, in.HostAdapterAvailable = sh.host, hostAdapterAvailable(sh.host)
	in.Host = hostFacts(sh.hostDoc)
	in.PublishLive = sh.xws.Mode == "live"
	if p := d.Mapping.primary(); p != nil {
		for _, e := range sh.xws.Allowlist {
			in.Allowlisted = in.Allowlisted || e.ConnectionID == p.ConnectionID && e.ProviderProjectID == p.ProviderProjectID
		}
		in.WriteCredential = "none"
		if pc, ok := sh.wcreds[p.ConnectionID]; ok && pc.State == "set" {
			in.WriteCredential = "set"
			if pc.LastCheck != nil {
				in.WriteCredential = pc.LastCheck.Status
			}
		}
		if c, ok := conns[p.ConnectionID]; ok && c.Capabilities != nil {
			for _, sc := range c.Capabilities.TokenScopes {
				in.ReadHasWriteScope = in.ReadHasWriteScope || sc == "api" || sc == "write_repository"
			}
		}
	}
	in.Jev = readinessSecret(sh.secrets["jev"])
	in.BackendKeys = map[string]readiness.Secret{}
	for _, k := range secretKinds {
		if k.Backend != "" {
			in.BackendKeys[k.Backend] = readinessSecret(sh.secrets[k.Kind])
		}
	}
	if d := sh.state.doctorFor(sh.host); d != nil { // only the effective host's doctor counts
		doc := &readiness.Doctor{}
		for _, c := range d.Checks {
			doc.Checks = append(doc.Checks, readiness.DoctorCheck{ID: c.ID, State: c.State})
		}
		in.Doctor = doc
	}
	return in
}

// hostFacts turns the stored probe into readiness facts.
func hostFacts(d *HostStateDoc) *readiness.HostFacts {
	if d == nil || d.LastProbe == nil {
		return nil
	}
	p := d.LastProbe
	f := &readiness.HostFacts{Probed: true, Fake: p.Fake, SpikeAttested: d.Spike != nil}
	if p.Status != "ok" {
		f.Reasons = append(f.Reasons, p.Reasons...)
		if len(f.Reasons) == 0 {
			f.Reasons = []string{"host_codex_missing"}
		}
	} else if time.Since(p.CheckedAt) > 24*time.Hour {
		return nil // an old probe counts as no probe
	}
	return f
}
