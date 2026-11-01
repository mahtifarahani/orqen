package server

import (
	"context"
	"orqen/internal/providers"
	"strings"
	"time"
)

// adapterState carries what the four adapter stages learned, for the final snapshot write.
type adapterState struct {
	identity *providers.Identity
	caps     *providers.Capabilities
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func (s *adapterState) identitySnapshot(at time.Time) *ProviderIdentity {
	if s.identity == nil {
		return nil
	}
	return &ProviderIdentity{ProviderUserID: s.identity.ProviderUserID, Username: s.identity.Username, DisplayName: optional(s.identity.DisplayName), VerifiedAt: at}
}
func (s *adapterState) capabilitySnapshot(at time.Time) *CapabilitySnapshot {
	if s.caps == nil {
		return nil
	}
	return &CapabilitySnapshot{ServerVersion: optional(s.caps.ServerVersion), DeploymentDetected: optional(s.caps.Deployment), TokenScopes: s.caps.TokenScopes, Features: s.caps.Features, CheckedAt: at}
}
func providerStage(name string, err error) StageResult {
	if e, ok := providers.AsError(err); ok {
		return stage(name, "failed", e.Code, e.Message)
	}
	return stage(name, "failed", "request_failed", "the request failed")
}

// adapterStage runs auth, identity, capabilities and sample_access against the provider REST API.
func (a *App) adapterStage(ctx context.Context, name string, conn ConnectionDoc, done []StageResult, st *adapterState) StageResult {
	status := func(stageName string) string {
		for _, r := range done {
			if r.Stage == stageName {
				return r.Status
			}
		}
		return ""
	}
	if status("network") == "failed" || status("tls") == "failed" || status("http") == "failed" {
		return stage(name, "skipped", "previous_stage_failed", "a connectivity stage failed")
	}
	switch status("credential") {
	case "passed":
	case "failed":
		return stage(name, "skipped", "previous_stage_failed", "the credential stage failed")
	default:
		return stage(name, "skipped", "no_credential", "no credential is set")
	}
	switch name {
	case "auth":
		var id providers.Identity
		var err error
		if conn.Provider == "gitlab" {
			var host providers.CodeHost
			if host, err = a.codeHost(ctx, conn); err == nil {
				id, err = host.Identity(ctx)
			}
		} else {
			var src providers.IssueSource
			if src, err = a.issueSource(ctx, conn); err == nil {
				id, err = src.Identity(ctx)
			}
		}
		if err != nil {
			return providerStage(name, err)
		}
		st.identity = &id
		return stage(name, "passed", "", "the credential was accepted")
	case "identity":
		if st.identity == nil {
			return stage(name, "skipped", "previous_stage_failed", "authentication failed")
		}
		if st.identity.ProviderUserID == "" {
			return stage(name, "failed", "identity_missing", "the provider returned no user id")
		}
		return stage(name, "passed", "", "authenticated as "+first(st.identity.Username, st.identity.DisplayName, st.identity.ProviderUserID))
	case "capabilities":
		if st.identity == nil {
			return stage(name, "skipped", "previous_stage_failed", "authentication failed")
		}
		var caps providers.Capabilities
		var err error
		if conn.Provider == "gitlab" {
			var host providers.CodeHost
			if host, err = a.codeHost(ctx, conn); err == nil {
				caps, err = host.Capabilities(ctx)
			}
		} else {
			var src providers.IssueSource
			if src, err = a.issueSource(ctx, conn); err == nil {
				caps, err = src.Capabilities(ctx)
			}
		}
		if err != nil {
			return providerStage(name, err)
		}
		st.caps = &caps
		required := "list_projects"
		if conn.Provider == "jira" {
			required = "browse_projects"
		}
		if caps.Features[required] == providers.FeatureDenied {
			return stage(name, "failed", "capability_missing", "the credential cannot "+strings.ReplaceAll(required, "_", " "))
		}
		return stage(name, "passed", "", "capabilities recorded")
	default:
		return a.sampleAccess(ctx, name, conn, st)
	}
}
func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
func (a *App) sampleAccess(ctx context.Context, name string, conn ConnectionDoc, st *adapterState) StageResult {
	if st.identity == nil {
		return stage(name, "skipped", "previous_stage_failed", "authentication failed")
	}
	if conn.Provider == "jira" {
		src, err := a.issueSource(ctx, conn)
		if err == nil {
			_, err = src.ListProjects(ctx, "", 0, 1)
		}
		if err != nil {
			return providerStage(name, err)
		}
		return stage(name, "passed", "", "listed Jira projects")
	}
	host, err := a.codeHost(ctx, conn)
	var page providers.ProjectsPage
	if err == nil {
		page, err = host.ListProjectsPage(ctx, "")
	}
	if err != nil {
		return providerStage(name, err)
	}
	if len(page.Items) == 0 {
		return stage(name, "passed", "", "listed GitLab projects (none visible)")
	}
	p := page.Items[0]
	if _, err = host.ListTree(ctx, p.ID, first(p.DefaultBranch, "HEAD"), ""); err != nil {
		if e, ok := providers.AsError(err); ok && e.Kind == providers.KindPermissionDenied {
			if st.caps != nil {
				st.caps.Features["read_repository_files"] = providers.FeatureDenied
			}
			return stage(name, "failed", "repo_read_denied", "the credential cannot read repository files")
		}
		return providerStage(name, err)
	}
	return stage(name, "passed", "", "listed projects and read a repository tree")
}
