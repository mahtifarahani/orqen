package catherd

import (
	"context"
	"encoding/json"
)

type ProfileRow struct {
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Repos  []string `json:"repos"`
}
type SetOutcome struct {
	Saved   bool
	Refused []Issue
}
type Validation struct {
	Valid    bool
	Errors   []Issue
	Warnings []Issue
}

// Error is a catherd failure. Code is catherd's E_* code or an orqen_* code.
type Error struct {
	Code, Message, Fix string
	Exit               int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

const (
	CodeUnavailable = "orqen_unavailable"
	CodeTimeout     = "orqen_timeout"
	CodeBadOutput   = "orqen_bad_output"
	CodeRejected    = "catherd_rejected_input"
)

// RunsPort is the run side of catherd: reads of run state and the two commands that need no model.
type RunsPort interface {
	// RunsShow reads `runs show <id> --json --debug`: records, dispatches and their records.
	RunsShow(ctx context.Context, env *OrgEnv, runID string) (RunView, error)
	// RunsCancel stops one live role (`runs cancel <id> <name>`): interrupt, SIGTERM, SIGKILL, recorded cancelled.
	RunsCancel(ctx context.Context, env *OrgEnv, runID, name string) error
	// ProfileUseRepo binds a profile to the git repo at repoDir (`profile use <name> --repo`, run from there).
	ProfileUseRepo(ctx context.Context, env *OrgEnv, host, name, repoDir string) error
}

// Port is every catherd call Orqen makes. Write methods take only *OrgEnv, so personal state cannot be written, and
// only the methods with a SecretEnv parameter can receive a secret (PersonalEnv has no injection path at all).
type Port interface {
	Version(ctx context.Context) (string, error)
	ListProfiles(ctx context.Context, env ReadEnv) ([]ProfileRow, error)
	ShowRaw(ctx context.Context, env ReadEnv, host, name string) (map[string]any, error)
	ShowResolved(ctx context.Context, env *OrgEnv, host, name string) (map[string]any, error)
	Create(ctx context.Context, env *OrgEnv, host, name string, sec SecretEnv) error
	SetKey(ctx context.Context, env *OrgEnv, host, name, key string, value any, sec SecretEnv) (SetOutcome, error)
	Validate(ctx context.Context, env *OrgEnv, host, name string, sec SecretEnv) (Validation, error)
	Remove(ctx context.Context, env *OrgEnv, name string) error
	CatalogList(ctx context.Context, env *OrgEnv) ([]byte, error)
	CatalogRefresh(ctx context.Context, env *OrgEnv, sec SecretEnv) (json.RawMessage, error)
	CatalogSync(ctx context.Context, env *OrgEnv, force bool, sec SecretEnv) (json.RawMessage, error)
	// Doctor returns the report and catherd's exit code (0 ready, 3 not ready).
	Doctor(ctx context.Context, env *OrgEnv, host string, sec SecretEnv) (json.RawMessage, int, error)
	RunsPort
}
