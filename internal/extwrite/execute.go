package extwrite

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"orqen/internal/workspace"
)

// ErrOutcomeUnknown means the write may or may not have happened (timeout, reset, a 5xx after the send).
var ErrOutcomeUnknown = errors.New("extwrite: the outcome of the write is unknown")

// Exec describes one write for Run.
type Exec struct {
	Action    Action
	BaseURL   string // the connection's base URL
	ProjectID string // provider project id
	RepoURL   string // https (or loopback http) clone URL, no userinfo
	Dir       string // the worktree, for pushes
	Git       *workspace.Git
	Branch    string
	SHA       string
	LeaseSHA  string
	MRIID     int
	Marker    string
	MR        MRParams
	Body      string
	Position  *Position
	// Reconcile makes Run look for the effect first (a retry or a recovery): never a blind second write.
	Reconcile bool
	// LookOnly only looks for the effect (after an unknown outcome): it never writes. Not found is ErrNotFoundRemote.
	LookOnly bool
}

// ErrNotFoundRemote means a look-only reconcile found no trace of the write.
var ErrNotFoundRemote = errors.New("extwrite: no trace of the write on the remote")

// Result is what the remote now has.
type Result struct {
	MRIID        int
	WebURL       string
	NoteID       int
	DiscussionID string
	PushedSHA    string
	Reconciled   bool // the effect already existed
}

// classify turns a transport error into a definite or an unknown outcome.
func classify(err error) error {
	var se *StatusError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotLoopback), errors.Is(err, ErrForeignPush):
		return err
	case errors.As(err, &se):
		if se.Status >= 500 {
			return errors.Join(ErrOutcomeUnknown, err)
		}
		return err
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "connection reset") {
		return errors.Join(ErrOutcomeUnknown, err)
	}
	return err
}

// WriteTimeout bounds each write attempt (tests shorten it).
var WriteTimeout = 30 * time.Second

// Run performs one write inside a Permit (so only after the gate decided execute twice). A 30 second timeout covers
// each attempt.
func Run(ctx context.Context, p *Permit, in Exec) (Result, error) {
	token, err := p.Credential()
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	g := &GitLab{Base: in.BaseURL, Token: token, HC: p.Client(), Scope: p.Scope()}
	var res Result
	switch in.Action {
	case GitPush:
		if in.Reconcile || in.LookOnly {
			if sha, e := LsRemote(ctx, p, in.Git, in.Dir, in.RepoURL, token, in.Branch); e == nil && sha == in.SHA {
				return Result{PushedSHA: sha, Reconciled: true}, nil
			}
		}
		if in.LookOnly {
			return res, ErrNotFoundRemote
		}
		if err = Push(ctx, p, PushInput{Git: in.Git, Dir: in.Dir, URL: in.RepoURL, Token: token, Branch: in.Branch, SHA: in.SHA, LeaseSHA: in.LeaseSHA}); err != nil {
			return res, classifyGit(err)
		}
		return Result{PushedSHA: in.SHA}, nil
	case MRCreate:
		if in.Reconcile || in.LookOnly {
			mr, e := g.FindMRByMarker(ctx, in.ProjectID, in.MR.SourceBranch, in.Marker)
			if e != nil {
				return res, classify(e)
			}
			if mr != nil {
				return Result{MRIID: mr.IID, WebURL: mr.WebURL, Reconciled: true}, nil
			}
		}
		if in.LookOnly {
			return res, ErrNotFoundRemote
		}
		mr, e := g.CreateMR(ctx, in.ProjectID, in.MR)
		if e != nil {
			return res, classify(e)
		}
		return Result{MRIID: mr.IID, WebURL: mr.WebURL}, nil
	case MRUpdate:
		if in.LookOnly {
			return res, ErrNotFoundRemote // an update is idempotent: it is simply repeated
		}
		return res, classify(g.UpdateMRDescription(ctx, in.ProjectID, in.MRIID, in.Body))
	case MRNote:
		if in.Reconcile || in.LookOnly {
			notes, e := g.ListNotes(ctx, in.ProjectID, in.MRIID)
			if e != nil {
				return res, classify(e)
			}
			for _, n := range notes {
				if strings.Contains(n.Body, in.Marker) {
					return Result{NoteID: n.ID, Reconciled: true}, nil
				}
			}
		}
		if in.LookOnly {
			return res, ErrNotFoundRemote
		}
		n, e := g.CreateNote(ctx, in.ProjectID, in.MRIID, in.Body)
		if e != nil {
			return res, classify(e)
		}
		return Result{NoteID: n.ID}, nil
	case MRDiscussion:
		if in.Reconcile || in.LookOnly {
			ds, e := g.ListDiscussions(ctx, in.ProjectID, in.MRIID)
			if e != nil {
				return res, classify(e)
			}
			for _, d := range ds {
				for _, n := range d.Notes {
					if strings.Contains(n.Body, in.Marker) {
						return Result{DiscussionID: d.ID, Reconciled: true}, nil
					}
				}
			}
		}
		if in.LookOnly {
			return res, ErrNotFoundRemote
		}
		d, e := g.CreateDiscussion(ctx, in.ProjectID, in.MRIID, in.Body, in.Position)
		if e != nil {
			return res, classify(e)
		}
		return Result{DiscussionID: d.ID}, nil
	}
	return res, errors.New("extwrite: action not supported")
}

func classifyGit(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotLoopback), errors.Is(err, ErrForeignPush):
		return err
	case errors.Is(err, workspace.ErrNetwork):
		return errors.Join(ErrOutcomeUnknown, err)
	case errors.Is(err, workspace.ErrAuth):
		return &StatusError{Status: 401, Body: "git authentication failed"}
	}
	return err
}
