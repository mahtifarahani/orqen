package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/catherd"
	"orqen/internal/claudehost"
	"orqen/internal/codexhost"
	"orqen/internal/execution"
)

// requiredCatherdTools are the catherd MCP tools the directed host protocol calls.
var requiredCatherdTools = []string{"cancel", "dispatch", "land", "profile_get", "result", "route", "run_start"}

func (a *App) hostState(ctx context.Context, org string) (HostStateDoc, error) {
	d, err := findOne[HostStateDoc](ctx, a.DB, "host_states", bson.M{"_id": hostStateID(org), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return HostStateDoc{ID: hostStateID(org), OrgID: org}, nil
	}
	return d, err
}

// HostStatusDTO is HostStatusDto.
func (a *App) hostStatusDTO(ctx context.Context, d HostStateDoc) bson.M {
	var probe, spike any
	if d.LastProbe != nil {
		p := d.LastProbe
		probe = bson.M{"status": p.Status, "codex_version": optStr(p.CodexVersion), "login": p.Login, "catherd_tools": nonNilStr(p.CatherdTools), "fake": p.Fake, "checked_at": timestamp(p.CheckedAt), "reasons": nonNilStr(p.Reasons)}
	}
	if s := d.Spike; s != nil {
		spike = bson.M{"at": timestamp(s.At), "codex_version": s.CodexVersion, "catherd_version": s.CatherdVersion, "note": optStr(s.Note)}
	}
	return bson.M{"probe": probe, "spike": spike, "available": a.hostAvailable(ctx, d.OrgID), "min_codex_version": codexhost.MinCodexVersion, "revision": d.Revision}
}

func (a *App) getHost(w http.ResponseWriter, r *http.Request) error {
	d, err := a.hostState(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, 200, a.hostStatusDTO(r.Context(), d))
	return nil
}

func (a *App) probeHost(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/host/probe"
	hash := requestHash("POST", route, map[string]string{"orgId": org}, nil)
	return a.idempotent(w, r, org, route, key, hash, func(ctx context.Context) (Output, error) {
		return a.startOperation(ctx, org, "host_probe", Subject{"host", hostStateID(org)}, "host_probe:"+org, 1, bson.M{})
	})
}

// versionAtLeast compares dotted numeric versions.
func versionAtLeast(have, min string) bool {
	parse := func(s string) [3]int {
		var v [3]int
		s = strings.SplitN(strings.SplitN(s, "-", 2)[0], "+", 2)[0]
		for i, p := range strings.SplitN(s, ".", 3) {
			n := 0
			for _, c := range p {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			v[i] = n
		}
		return v
	}
	h, m := parse(have), parse(min)
	for i := range h {
		if h[i] != m[i] {
			return h[i] > m[i]
		}
	}
	return true
}

// evaluateProbe turns what the app-server reported into the stored probe.
func evaluateProbe(res codexhost.ProbeResult) HostProbe {
	return evaluateHostProbe(catherd.HostCodex, res)
}

// evaluateHostProbe is evaluateProbe for one orchestration host. For Claude Code the stored version field carries the
// Claude Code version (the field keeps its name so the stored shape and the API stay the same).
func evaluateHostProbe(host string, res codexhost.ProbeResult) HostProbe {
	p := HostProbe{Status: "ok", CodexVersion: res.Version(), CheckedAt: time.Now().UTC(), Login: "missing", Reasons: []string{}}
	p.Fake = strings.HasPrefix(res.UserAgent, "orqen_fake_codex") || strings.HasPrefix(res.UserAgent, "orqen_fake_claude")
	if res.LoggedIn {
		p.Login = "ok"
	}
	minVersion := codexhost.MinCodexVersion
	if host == catherd.HostClaudeCode {
		minVersion = claudehost.MinClaudeVersion
	}
	if p.CodexVersion == "" || !versionAtLeast(p.CodexVersion, minVersion) {
		if host == catherd.HostClaudeCode {
			p.Reasons = append(p.Reasons, "host_claude_too_old")
		} else {
			p.Reasons = append(p.Reasons, "host_codex_too_old")
		}
	}
	if !res.LoggedIn {
		p.Reasons = append(p.Reasons, "host_login_missing")
	}
	have := map[string]bool{}
	for _, s := range res.MCPServers {
		if s.Name == "catherd" {
			for _, t := range s.Tools {
				have[t] = true
				p.CatherdTools = append(p.CatherdTools, t)
			}
		}
	}
	sort.Strings(p.CatherdTools)
	for _, t := range requiredCatherdTools {
		if !have[t] {
			p.Reasons = append(p.Reasons, "host_catherd_mcp_missing")
			break
		}
	}
	if len(p.Reasons) > 0 {
		p.Status = "failed"
	}
	return p
}

// hostProbeConfig is the host configuration without a run: no pinned profile, only the base secrets.
func (a *App) hostProbeConfig(ctx context.Context, org string) (execution.HostConfig, error) {
	env, err := a.catherdEnv(org)
	if err != nil {
		return execution.HostConfig{}, err
	}
	if a.CatherdRT == nil || a.CatherdRT.Bin == "" {
		return execution.HostConfig{}, &JobFailure{Code: "catherd_unavailable", Message: "catherd is not available"}
	}
	sec, err := a.secretEnvFor(ctx, org, []string{"TYPESAFE_API_KEY"})
	if err != nil {
		sec = catherd.SecretEnv{}
	}
	return execution.HostConfig{Host: a.orgHost(ctx, org), CodexBin: a.Config.CodexBin, ClaudeBin: a.claudeBin(), CatherdBin: a.CatherdRT.Bin, DataDir: a.Config.DataDir, OrgID: org, Org: env, RT: a.CatherdRT, Secrets: sec}, nil
}

// runHostProbe starts the app-server (or reuses the running one), reads its state, and records the result. It creates
// no thread and no turn.
func (a *App) runHostProbe(ctx context.Context, job JobDoc, lease Lease) error {
	org := job.OrgID
	probe := HostProbe{Status: "failed", CheckedAt: time.Now().UTC(), Login: "missing", Reasons: []string{"host_codex_missing"}}
	hostName := a.orgHost(ctx, org)
	if hostName == catherd.HostClaudeCode {
		probe.Reasons = []string{"host_claude_missing"}
	}
	if a.HostSupervisor != nil {
		cfg, err := a.hostProbeConfig(ctx, org)
		if err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		client, err := a.HostSupervisor.Client(pctx, org, cfg)
		switch {
		case errors.Is(err, execution.ErrHostBusy):
			cancel()
			return &JobFailure{Code: "host_busy", Message: "another runner holds the orchestration host"}
		case err != nil:
			// Not started: codex is missing or refused to initialize.
		default:
			a.HostSupervisor.Pin(org)
			res, perr := client.Probe(pctx)
			a.HostSupervisor.Unpin(org)
			if perr == nil {
				probe = evaluateHostProbe(hostName, res)
			} else if hostName == catherd.HostClaudeCode {
				probe.Reasons = []string{"host_claude_missing"}
			} else {
				probe.Reasons = []string{"host_codex_missing"}
			}
		}
		cancel()
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		var cur HostStateDoc
		if _, e := a.DB.Coll("host_states").UpdateOne(sc, bson.M{"_id": hostStateID(org)}, bson.M{"$set": bson.M{"org_id": org, "last_probe": probe}, "$setOnInsert": bson.M{"spike_attested": nil}, "$inc": bson.M{"revision": int64(1)}}, upsertOpt()); e != nil {
			return e
		}
		cur, e := a.hostState(sc, org)
		if e != nil {
			return e
		}
		if e := a.AppendEvent(sc, org, "host.probed", Subject{"host", hostStateID(org)}, bson.M{"status": probe.Status, "reasons": nonNilStr(probe.Reasons)}, &cur.Revision, job.OperationID, true); e != nil {
			return e
		}
		if e := a.transitionOperation(sc, job, "succeeded", bson.M{"status": probe.Status, "reasons": nonNilStr(probe.Reasons)}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}

// PostHostSpikeAttestRequest is the body of POST /host/spike-attest.
type PostHostSpikeAttestRequest struct {
	CodexVersion   string  `json:"codex_version"`
	CatherdVersion string  `json:"catherd_version"`
	Note           *string `json:"note"`
}

// spikeAttest records the user's statement that the real spike (docs/LIVE-TRIAL.md) passed. Orqen never runs it.
func (a *App) spikeAttest(w http.ResponseWriter, r *http.Request) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	var req PostHostSpikeAttestRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if strings.TrimSpace(req.CodexVersion) == "" || utf8.RuneCountInString(req.CodexVersion) > 40 {
		return validation("codex_version", "is required")
	}
	if strings.TrimSpace(req.CatherdVersion) == "" || utf8.RuneCountInString(req.CatherdVersion) > 40 {
		return validation("catherd_version", "is required")
	}
	note := ""
	if req.Note != nil {
		if utf8.RuneCountInString(*req.Note) > 500 {
			return validation("note", "must be at most 500 characters")
		}
		note = *req.Note
	}
	org := r.PathValue("orgId")
	var doc HostStateDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		cur, err := a.hostState(ctx, org)
		if err != nil {
			return err
		}
		if cur.LastProbe == nil || cur.LastProbe.Status != "ok" {
			return problem("host_not_probed")
		}
		if cur.Revision != expected {
			c := problem("revision_conflict")
			c.Current = a.hostStatusDTO(ctx, cur)
			return c
		}
		spike := HostSpike{At: time.Now().UTC(), CodexVersion: req.CodexVersion, CatherdVersion: req.CatherdVersion, Note: note}
		if e := a.DB.Coll("host_states").FindOneAndUpdate(ctx, bson.M{"_id": hostStateID(org), "revision": expected}, bson.M{"$set": bson.M{"spike_attested": spike}, "$inc": bson.M{"revision": int64(1)}}, afterUpdate()).Decode(&doc); e != nil {
			if errors.Is(e, mongo.ErrNoDocuments) {
				return problem("revision_conflict")
			}
			return e
		}
		return a.AppendEvent(ctx, org, "host.spike_attested", Subject{"host", hostStateID(org)}, bson.M{"codex_version": spike.CodexVersion, "catherd_version": spike.CatherdVersion}, &doc.Revision, nil, false)
	})
	if err != nil {
		return err
	}
	a.Committed()
	w.Header().Set("ETag", etag(doc.Revision))
	writeJSON(w, 200, a.hostStatusDTO(r.Context(), doc))
	return nil
}
