package server

import (
	"context"
	"go.mongodb.org/mongo-driver/v2/bson"
	"log/slog"
	"net/http"
	"orqen/internal/catherd"
	"os"
	"os/exec"
	"path/filepath"
)

const hostPolicyKey = "orchestrator.host"

// orgHostE is the organization's effective orchestration host: the latest published org policy, else the product
// default. `auto` does not exist, so the host never depends on Orqen's own process environment. Jobs use this form so a
// database error stops the job instead of silently choosing a host.
func (a *App) orgHostE(ctx context.Context, org string) (string, error) {
	e, _, _, err := a.effectivePolicy(ctx, org, "")
	if err != nil {
		return "", err
	}
	for _, x := range e.Entries {
		if s, ok := x.Value.(string); ok && x.Key == hostPolicyKey && catherd.ValidHost(s) {
			return s, nil
		}
	}
	return catherd.HostCodex, nil
}

// orgHost is the display form of orgHostE: an error falls back to the default and is logged.
func (a *App) orgHost(ctx context.Context, org string) string {
	h, err := a.orgHostE(ctx, org)
	if err != nil {
		slog.Warn("could not read the orchestrator host policy", "error", err)
		return catherd.HostCodex
	}
	return h
}

// hostAdapterAvailable says whether Orqen has an execution adapter for the host: the Codex App Server (SPEC
// section 3) and headless Claude Code (internal/claudehost). Whether the host actually works on this machine is the
// probe's business (hostAvailable), not the adapter's.
func hostAdapterAvailable(host string) bool {
	return host == catherd.HostCodex || host == catherd.HostClaudeCode
}

// claudeBin is the claude executable: the configured path, else the first `claude` on the host's PATH.
func (a *App) claudeBin() string {
	if a.Config.ClaudeBin != "" {
		return a.Config.ClaudeBin
	}
	if a.CatherdRT != nil {
		for _, dir := range filepath.SplitList(a.CatherdRT.ChildPath) {
			p := filepath.Join(dir, "claude")
			if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
				return p
			}
		}
	}
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return ""
}

func doctorDTO(d *DoctorSummary) any {
	if d == nil {
		return nil
	}
	checks := d.Checks
	if checks == nil {
		checks = []DoctorCheck{}
	}
	host := d.Host
	if host == "" {
		host = catherd.HostCodex
	}
	return bson.M{"operation_id": d.OperationID, "checked_at": timestamp(d.CheckedAt), "ready": d.Ready, "checks": checks, "artifact_id": d.ArtifactID, "host": host}
}

// doctorFor is the doctor summary of one host. The legacy single last_doctor is the codex report.
func (s *CatherdStateDoc) doctorFor(host string) *DoctorSummary {
	if s == nil {
		return nil
	}
	if d, ok := s.Doctors[host]; ok {
		return &d
	}
	if host == catherd.HostCodex && s.LastDoctor != nil {
		d := *s.LastDoctor
		d.Host = catherd.HostCodex
		return &d
	}
	return nil
}
func (a *App) listOrchestratorHosts(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	effective, err := a.orgHostE(r.Context(), org)
	if err != nil {
		return err
	}
	st, err := a.catherdState(r.Context(), org)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, h := range catherd.Hosts {
		adapter := "unavailable"
		if hostAdapterAvailable(h) {
			adapter = "available"
		}
		out = append(out, bson.M{"host": h, "effective": h == effective, "catherd_supported": true, "orqen_host_adapter": adapter, "last_doctor": doctorDTO(st.doctorFor(h))})
	}
	writeJSON(w, 200, out)
	return nil
}
