package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"orqen/internal/providers"
	"time"
)

func checkResult(check, status, code, detail string) CheckResult {
	return CheckResult{Check: check, Status: status, Code: optionalString(code), Detail: detail}
}

// providerCheck turns a provider failure into a failed check. Transient failures abort the job so it is retried.
func providerCheck(check string, err error) (CheckResult, error) {
	e, ok := providers.AsError(err)
	if !ok {
		return CheckResult{}, err
	}
	if e.Retryable() {
		return CheckResult{}, err
	}
	code := e.Code
	switch {
	case e.Kind == providers.KindNotFound:
		code = "not_found"
	case e.Kind == providers.KindUnsupported:
		code = "capability_unsupported"
	}
	return checkResult(check, "failed", code, e.Message), nil
}

// runMappingCheck verifies a project's mapping against the providers. Snapshots are refreshed only if the mapping
// did not change while the checks ran; otherwise the stored hash makes readiness report the check as stale.
func (a *App) runMappingCheck(ctx context.Context, job JobDoc, lease Lease) error {
	if job.OperationID == nil {
		return &JobFailure{Code: "no_operation", Message: "job has no operation"}
	}
	id, ok := job.Payload["project_id"].(string)
	if !ok || !validID("prj", id) {
		return &JobFailure{Code: "bad_payload", Message: "missing project_id"}
	}
	if err := a.fenced(ctx, lease, func(sc context.Context) error { return a.transitionOperation(sc, job, "running", nil) }); err != nil {
		return err
	}
	proj, err := a.project(ctx, job.OrgID, id)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &JobFailure{Code: "project_archived", Message: "project no longer exists"}
	}
	if err != nil {
		return err
	}
	hash := proj.Mapping.Hash()
	repos := append([]RepoMapping{}, proj.Mapping.Repos...)
	jira := proj.Mapping.Jira
	var jiraCopy *JiraMapping
	if jira != nil {
		c := *jira
		jiraCopy = &c
	}
	results := []CheckResult{}
	add := func(r CheckResult, err error) error {
		if err != nil {
			return err
		}
		results = append(results, r)
		return nil
	}
	hostFor := func(connID string) (providers.CodeHost, CheckResult, bool) {
		conn, err := a.connection(ctx, job.OrgID, connID)
		if err != nil {
			return nil, checkResult("", "failed", "connection_unavailable", "the connection no longer exists"), false
		}
		host, err := a.codeHost(ctx, conn)
		if err != nil {
			code := "connection_unavailable"
			if e, ok := providers.AsError(err); ok {
				code = e.Code
			}
			return nil, checkResult("", "failed", code, err.Error()), false
		}
		return host, CheckResult{}, true
	}
	for i := range repos {
		repo := &repos[i]
		name := func(prefix string) string { return prefix + ":" + repo.ProviderProjectID }
		host, failed, ok := hostFor(repo.ConnectionID)
		if !ok {
			for _, n := range []string{"repo_access", "target_branch"} {
				failed.Check = name(n)
				results = append(results, failed)
			}
			continue
		}
		p, err := host.GetProject(ctx, repo.ProviderProjectID)
		if err != nil {
			if e := add(providerCheck(name("repo_access"), err)); e != nil {
				return e
			}
			for _, n := range []string{"target_branch", "package_path"} {
				if n == "package_path" && repo.PackagePath == nil {
					continue
				}
				results = append(results, checkResult(name(n), "skipped", "previous_check_failed", "the repository could not be read"))
			}
			continue
		}
		repo.PathSnapshot = p.PathWithNamespace
		repo.DefaultBranchSnapshot = optionalString(p.DefaultBranch)
		results = append(results, checkResult(name("repo_access"), "passed", "", "repository is readable"))
		if _, err = host.GetBranch(ctx, repo.ProviderProjectID, repo.TargetBranch); err != nil {
			if r, e := providerCheck(name("target_branch"), err); e != nil {
				return e
			} else {
				if r.Code != nil && *r.Code == "not_found" {
					r = checkResult(name("target_branch"), "failed", "branch_not_found", "branch "+repo.TargetBranch+" does not exist")
				}
				results = append(results, r)
			}
		} else {
			results = append(results, checkResult(name("target_branch"), "passed", "", "branch exists"))
		}
		if repo.PackagePath != nil {
			if _, err = host.ListTree(ctx, repo.ProviderProjectID, repo.TargetBranch, *repo.PackagePath); err != nil {
				if r, e := providerCheck(name("package_path"), err); e != nil {
					return e
				} else {
					if r.Code != nil && *r.Code == "not_found" {
						r = checkResult(name("package_path"), "failed", "package_path_not_found", "directory "+*repo.PackagePath+" does not exist on the target branch")
					}
					results = append(results, r)
				}
			} else {
				results = append(results, checkResult(name("package_path"), "passed", "", "directory exists"))
			}
		}
	}
	if j := jiraCopy; j != nil {
		conn, err := a.connection(ctx, job.OrgID, j.ConnectionID)
		var src providers.IssueSource
		if err == nil {
			src, err = a.issueSource(ctx, conn)
		}
		if err != nil {
			code := "connection_unavailable"
			if e, ok := providers.AsError(err); ok {
				code = e.Code
			}
			for _, n := range []string{"jira_project", "jira_board", "jql"} {
				if n == "jira_board" && j.Board == nil || n == "jql" && j.JQL == nil {
					continue
				}
				results = append(results, checkResult(n, "failed", code, err.Error()))
			}
		} else {
			jp, perr := src.GetProject(ctx, j.ProjectID)
			if perr != nil {
				if e := add(providerCheck("jira_project", perr)); e != nil {
					return e
				}
			} else {
				j.ProjectKeySnapshot = jp.Key
				results = append(results, checkResult("jira_project", "passed", "", "project "+jp.Key+" is readable"))
			}
			if j.Board != nil {
				b, berr := src.GetBoard(ctx, j.Board.ID)
				switch {
				case berr != nil:
					if e := add(providerCheck("jira_board", berr)); e != nil {
						return e
					}
				case b.ProjectID != "" && b.ProjectID != j.ProjectID:
					results = append(results, checkResult("jira_board", "failed", "board_project_mismatch", fmt.Sprintf("board %s belongs to project %s", b.ID, b.ProjectID)))
				default:
					j.Board.NameSnapshot = optionalString(b.Name)
					results = append(results, checkResult("jira_board", "passed", "", "board is readable"))
				}
			}
			if j.JQL != nil {
				if qerr := src.CheckJQL(ctx, *j.JQL); qerr != nil {
					if e := add(providerCheck("jql", qerr)); e != nil {
						return e
					}
				} else {
					results = append(results, checkResult("jql", "passed", "", "JQL is valid"))
				}
			}
		}
	}
	for i, ref := range proj.Mapping.Confluence {
		name := fmt.Sprintf("confluence_ref:%d", i)
		if ref.PageID == nil {
			results = append(results, checkResult(name, "skipped", "space_only_reference", "a space reference has no page to read"))
			continue
		}
		conn, err := a.connection(ctx, job.OrgID, ref.ConnectionID)
		var src providers.DocsSource
		if err == nil {
			src, err = a.docsSource(ctx, conn)
		}
		if err != nil {
			code := "connection_unavailable"
			if e, ok := providers.AsError(err); ok {
				code = e.Code
			}
			results = append(results, checkResult(name, "failed", code, err.Error()))
			continue
		}
		if _, err = src.GetPage(ctx, *ref.PageID); err != nil {
			if e := add(providerCheck(name, err)); e != nil {
				return e
			}
			continue
		}
		results = append(results, checkResult(name, "passed", "", "page is readable"))
	}
	failed := 0
	for _, r := range results {
		if r.Status == "failed" {
			failed++
		}
	}
	return a.fenced(ctx, lease, func(sc context.Context) error {
		current, err := a.project(sc, job.OrgID, id)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return a.transitionOperation(sc, job, "failed", bson.M{"code": "project_archived", "message": "project no longer exists"})
		}
		if err != nil {
			return err
		}
		set := bson.M{"mapping_check": MappingCheck{MappingHash: hash, CheckedAt: time.Now().UTC(), OperationID: *job.OperationID, Results: results}}
		if current.Mapping.Hash() == hash {
			set["mapping.repos"] = repos
			if jiraCopy != nil {
				set["mapping.jira"] = jiraCopy
			}
		}
		if _, err = a.DB.Coll("projects").UpdateOne(sc, prjFilter(job.OrgID, id), bson.M{"$set": set}); err != nil {
			return err
		}
		if err = a.transitionOperation(sc, job, "succeeded", bson.M{"mapping_hash": hash, "checks": len(results), "failed": failed}); err != nil {
			return err
		}
		if err = a.AppendEvent(sc, job.OrgID, "project.mapping_checked", Subject{"project", id}, bson.M{"failed": failed, "checks": len(results)}, nil, job.OperationID, true); err != nil {
			return err
		}
		return a.completeJob(sc, lease)
	})
}
