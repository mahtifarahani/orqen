package workspace

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status is the state of a worktree.
type Status struct {
	Clean     bool
	Modified  int
	Untracked int
	Ahead     int
	HeadSHA   string
}

// Worktree is a prepared run workspace.
type Worktree struct {
	Path, Branch, BaseRef, BaseSHA string
}

// EnsureMirror creates the bare mirror when missing and points origin at remoteURL (which must carry no credentials).
func (m *Manager) EnsureMirror(ctx context.Context, connID, pid, remoteURL string) (string, error) {
	if _, err := m.CheckURL(remoteURL); err != nil {
		return "", err
	}
	mirror, err := m.MirrorPath(connID, pid)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(mirror), 0o700); err != nil {
		return "", err
	}
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return "", err
	}
	defer unlock()
	if _, err = os.Stat(filepath.Join(mirror, "HEAD")); err != nil {
		if _, err = m.Git.Run(ctx, m.Root, Creds{}, 0, "init", "--bare", "-q", mirror); err != nil {
			return "", err
		}
	}
	cur, err := m.Git.Run(ctx, mirror, Creds{}, 0, "config", "--get", "remote.origin.url")
	switch {
	case err != nil:
		_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "remote", "add", "origin", remoteURL)
	case strings.TrimSpace(cur.Stdout) != remoteURL:
		_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "remote", "set-url", "origin", remoteURL)
	}
	if err != nil {
		return mirror, err
	}
	// Every worktree shares this config: a plain `git push origin` from a role can never reach the remote.
	_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "config", "remote.origin.pushurl", PushBlockedURL)
	return mirror, err
}

// PushBlockedURL is the push URL of every mirror remote: git has no remote helper for it, so any push fails.
const PushBlockedURL = "orqen-blocked://disabled"

var refSafe = strings.NewReplacer(" ", "", "\n", "", "\x00", "", "..", "", "~", "", "^", "", ":", "", "?", "", "*", "", "[", "", "\\", "")

// Fetch updates only the target branch of the mirror.
func (m *Manager) Fetch(ctx context.Context, mirror, target string, c Creds) error {
	if err := m.inside(mirror); err != nil {
		return err
	}
	if target == "" || refSafe.Replace(target) != target || strings.HasPrefix(target, "-") {
		return ErrBadURL
	}
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	timeout := m.Git.FetchTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	_, err = m.Git.Run(ctx, mirror, c, timeout, "fetch", "--no-tags", "--prune", "origin", "+refs/heads/"+target+":refs/remotes/origin/"+target)
	return err
}

// registered returns the worktree paths and their branches registered in the mirror.
func (m *Manager) registered(ctx context.Context, mirror string) (map[string]string, error) {
	res, err := m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var path string
	for _, line := range strings.Split(res.Stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			out[path] = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	return out, nil
}

// AddWorktree creates (or reuses) the run's worktree on a new branch from origin/<target>.
func (m *Manager) AddWorktree(ctx context.Context, mirror, runID, branch, target string) (Worktree, error) {
	if !branchPart.MatchString(branch) {
		return Worktree{}, ErrBadURL
	}
	path, err := m.RunPath(runID)
	if err != nil {
		return Worktree{}, err
	}
	if err = m.inside(mirror); err != nil {
		return Worktree{}, err
	}
	baseRef := "refs/remotes/origin/" + target
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return Worktree{}, err
	}
	defer unlock()
	sha, err := m.Git.Run(ctx, mirror, Creds{}, 0, "rev-parse", "--verify", baseRef)
	if err != nil {
		return Worktree{}, ErrNotFound
	}
	wt := Worktree{Path: path, Branch: branch, BaseRef: baseRef, BaseSHA: strings.TrimSpace(sha.Stdout)}
	reg, err := m.registered(ctx, mirror)
	if err != nil {
		return Worktree{}, err
	}
	if b, ok := reg[path]; ok && b == branch {
		if _, e := os.Stat(path); e == nil {
			return wt, nil // a crash after the add: reuse it
		}
	}
	// Leftovers of a partial add belong to us: clear them and add again.
	_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "prune")
	if _, e := os.Lstat(path); e == nil {
		if err = m.inside(path); err != nil {
			return Worktree{}, err
		}
		if err = os.RemoveAll(path); err != nil {
			return Worktree{}, err
		}
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Worktree{}, err
	}
	if _, e := m.Git.Run(ctx, mirror, Creds{}, 0, "rev-parse", "--verify", "refs/heads/"+branch); e == nil {
		_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "add", "--force", path, branch)
	} else {
		_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "add", "--no-track", "-b", branch, path, baseRef)
	}
	return wt, err
}

// Inspect reports the worktree's status relative to baseSHA.
func (m *Manager) Inspect(ctx context.Context, path, baseSHA string) (Status, error) {
	if err := m.inside(path); err != nil {
		return Status{}, err
	}
	st := Status{}
	res, err := m.Git.Run(ctx, path, Creds{}, 0, "status", "--porcelain=v1", "-z")
	if err != nil {
		return st, err
	}
	for _, entry := range strings.Split(res.Stdout, "\x00") {
		switch {
		case entry == "":
		case strings.HasPrefix(entry, "??"):
			st.Untracked++
		default:
			st.Modified++
		}
	}
	head, err := m.Git.Run(ctx, path, Creds{}, 0, "rev-parse", "HEAD")
	if err != nil {
		return st, err
	}
	st.HeadSHA = strings.TrimSpace(head.Stdout)
	if baseSHA != "" {
		if n, e := m.Git.Run(ctx, path, Creds{}, 0, "rev-list", "--count", baseSHA+"..HEAD"); e == nil {
			st.Ahead, _ = strconv.Atoi(strings.TrimSpace(n.Stdout))
		}
	}
	st.Clean = st.Modified == 0 && st.Untracked == 0
	return st, nil
}

// Snapshot writes a tar.gz with changes.diff (git diff --binary against baseSHA) and every untracked file.
func (m *Manager) Snapshot(ctx context.Context, path, baseSHA string, w io.Writer) error {
	if err := m.inside(path); err != nil {
		return err
	}
	diff, err := m.Git.Run(ctx, path, Creds{}, 0, "diff", "--binary", baseSHA)
	if err != nil {
		return err
	}
	list, err := m.Git.Run(ctx, path, Creds{}, 0, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	if err = add("changes.diff", 0o644, []byte(diff.Stdout)); err != nil {
		return err
	}
	for _, rel := range strings.Split(list.Stdout, "\x00") {
		if rel == "" || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
			continue
		}
		full := filepath.Join(path, rel)
		info, e := os.Lstat(full)
		if e != nil || !info.Mode().IsRegular() {
			continue
		}
		if m.inside(full) != nil {
			continue
		}
		data, e := os.ReadFile(full)
		if e != nil {
			continue
		}
		if err = add("untracked/"+rel, int64(info.Mode().Perm()), data); err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// Remove deletes the worktree and its branch. With requireClean it refuses (ErrDirty) unless the worktree has no
// changes, no untracked files and HEAD equals baseSHA; without it, the removal is forced (Discard, after a snapshot).
func (m *Manager) Remove(ctx context.Context, mirror, path, branch, baseSHA string, requireClean bool) error {
	if err := m.inside(path); err != nil {
		return err
	}
	if err := m.inside(mirror); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if requireClean {
			st, e := m.Inspect(ctx, path, baseSHA)
			if e != nil {
				return e
			}
			if !st.Clean || baseSHA != "" && st.HeadSHA != baseSHA {
				return ErrDirty
			}
		}
	}
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	args := []string{"worktree", "remove"}
	if !requireClean {
		args = append(args, "--force")
	}
	if _, statErr := os.Stat(path); statErr == nil {
		if _, err = m.Git.Run(ctx, mirror, Creds{}, 0, append(args, path)...); err != nil {
			return err
		}
	} else {
		_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "prune")
	}
	if branchPart.MatchString(branch) {
		if _, e := m.Git.Run(ctx, mirror, Creds{}, 0, "rev-parse", "--verify", "refs/heads/"+branch); e == nil {
			_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "branch", "-D", branch)
		}
	}
	return err
}

var shaPart = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// VerifyPath is the detached verification checkout of a run at a commit (never the coder's worktree).
func (m *Manager) VerifyPath(runID, sha string) (string, error) {
	if !idPart.MatchString(runID) || !shaPart.MatchString(sha) {
		return "", ErrUnsafePath
	}
	p := filepath.Join(m.Root, "verify", runID+"-"+sha[:12])
	return p, m.inside(p)
}

// AddDetached checks the commit out at dir without a branch. The commit must exist in the mirror (worktrees share
// its object store, so commits made in a run's worktree do).
func (m *Manager) AddDetached(ctx context.Context, mirror, dir, sha string) error {
	if !shaPart.MatchString(sha) {
		return ErrBadURL
	}
	if err := m.inside(mirror); err != nil {
		return err
	}
	if err := m.inside(dir); err != nil {
		return err
	}
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "prune")
	if _, e := os.Lstat(dir); e == nil {
		_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "remove", "--force", dir)
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	_, err = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "add", "--detach", "--force", dir, sha)
	return err
}

// RemoveDetached deletes a verification checkout.
func (m *Manager) RemoveDetached(ctx context.Context, mirror, dir string) error {
	if err := m.inside(dir); err != nil {
		return err
	}
	if err := m.inside(mirror); err != nil {
		return err
	}
	unlock, err := lockFile(mirror + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "remove", "--force", dir)
	_, _ = m.Git.Run(ctx, mirror, Creds{}, 0, "worktree", "prune")
	return os.RemoveAll(dir)
}
