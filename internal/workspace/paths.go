package workspace

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

var (
	idPart     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,80}$`)
	branchPart = regexp.MustCompile(`^orqen/[a-z0-9._-]{1,64}/[a-z0-9]{1,32}$`)
)

// Manager works inside Root (<data>/workspaces/<org>). Nothing outside Root is read or written.
type Manager struct {
	Root         string
	Git          *Git
	MinFreeBytes int64
	// Statfs reports free bytes at a path; tests inject it.
	Statfs func(path string) (uint64, error)
}

func defaultStatfs(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// inside reports whether p, with symlinks resolved as far as it exists, lies within Root.
func (m *Manager) inside(p string) error {
	root, err := filepath.EvalSymlinks(m.Root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ErrUnsafePath
	}
	// Resolve the longest existing prefix so a symlinked parent cannot lead outside.
	existing, rest := abs, ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return ErrUnsafePath
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return ErrUnsafePath
	}
	full := filepath.Join(resolved, rest)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return ErrUnsafePath
	}
	return nil
}

// MirrorPath is the bare mirror of a provider project.
func (m *Manager) MirrorPath(connID, pid string) (string, error) {
	if !idPart.MatchString(connID) || !idPart.MatchString(pid) {
		return "", ErrUnsafePath
	}
	p := filepath.Join(m.Root, "mirrors", connID, pid+".git")
	return p, m.inside(p)
}

// RunPath is the worktree directory of a run.
func (m *Manager) RunPath(runID string) (string, error) {
	if !idPart.MatchString(runID) {
		return "", ErrUnsafePath
	}
	p := filepath.Join(m.Root, "runs", runID)
	return p, m.inside(p)
}

// Rel returns p relative to the data directory's parent of Root (workspaces/<org>/...), for storage.
func (m *Manager) Rel(p string) string {
	base := filepath.Dir(filepath.Dir(m.Root)) // <data>
	if rel, err := filepath.Rel(base, p); err == nil {
		return rel
	}
	return p
}

// CheckURL accepts an https clone URL without userinfo (http only for a loopback base, file only in tests).
func (m *Manager) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Scheme != "file" {
		return nil, ErrBadURL
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: it carries credentials", ErrBadURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !m.Git.AllowHTTP {
			return nil, fmt.Errorf("%w: plain http", ErrBadURL)
		}
	case "file":
		if !m.Git.AllowFile {
			return nil, fmt.Errorf("%w: file remote", ErrBadURL)
		}
	default:
		return nil, ErrBadURL
	}
	return u, nil
}

// CheckDisk fails with ErrDiskLow when less than MinFreeBytes is free.
func (m *Manager) CheckDisk() error {
	stat := m.Statfs
	if stat == nil {
		stat = defaultStatfs
	}
	if err := os.MkdirAll(m.Root, 0o700); err != nil {
		return err
	}
	free, err := stat(m.Root)
	if err != nil {
		return err
	}
	if m.MinFreeBytes > 0 && int64(free) < m.MinFreeBytes {
		return ErrDiskLow
	}
	return nil
}

// lockFile takes an exclusive cross-process lock for a mirror. The caller unlocks.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
