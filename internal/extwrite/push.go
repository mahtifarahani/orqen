package extwrite

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"orqen/internal/workspace"
)

// ErrForeignPush means the remote branch is not where Orqen last left it: someone else pushed.
var ErrForeignPush = errors.New("extwrite: the remote branch moved (a foreign push)")

// PushInput is one push. URL must carry no credentials; the token reaches git through its environment only.
type PushInput struct {
	Git      *workspace.Git
	Dir      string // the run's worktree (it holds the commit)
	URL      string
	Token    string
	Branch   string
	SHA      string
	LeaseSHA string // the sha Orqen last pushed ("" = the branch must not exist)
}

func credsOf(rawURL, token string) (workspace.Creds, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return workspace.Creds{}, ErrNotLoopback
	}
	return workspace.Creds{Host: u.Host, Username: "oauth2", Password: token}, nil
}

// Push sends one commit to one branch with an explicit lease. The git runner is the Sprint 3 one (own HOME, no global
// config, no credential helper); in a loopback-only scope plain http to an IP literal is the only thing it may do.
func Push(ctx context.Context, p *Permit, in PushInput) error {
	if err := p.CheckURL(in.URL, true); err != nil {
		return err
	}
	c, err := credsOf(in.URL, in.Token)
	if err != nil {
		return err
	}
	g := *in.Git
	if p.Scope() == ScopeLoopbackOnly {
		g.AllowHTTP = true
		g.Proxy = ""
	}
	ref := "refs/heads/" + in.Branch
	_, err = g.Run(ctx, in.Dir, c, 0, "push", in.URL, in.SHA+":"+ref, "--force-with-lease="+ref+":"+in.LeaseSHA)
	if err != nil {
		msg := err.Error()
		if errors.Is(err, workspace.ErrGit) && (strings.Contains(msg, "rejected") || strings.Contains(msg, "stale info") || strings.Contains(msg, "non-fast-forward")) {
			return ErrForeignPush
		}
		return err
	}
	return nil
}

// LsRemote returns the sha of a remote branch, or "" when it does not exist.
func LsRemote(ctx context.Context, p *Permit, g0 *workspace.Git, dir, rawURL, token, branch string) (string, error) {
	if err := p.CheckURL(rawURL, true); err != nil {
		return "", err
	}
	c, err := credsOf(rawURL, token)
	if err != nil {
		return "", err
	}
	g := *g0
	if p.Scope() == ScopeLoopbackOnly {
		g.AllowHTTP = true
		g.Proxy = ""
	}
	res, err := g.Run(ctx, dir, c, 0, "ls-remote", rawURL, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) >= 1 {
		return fields[0], nil
	}
	return "", nil
}
