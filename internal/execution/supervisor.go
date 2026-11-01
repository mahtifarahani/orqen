package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"orqen/internal/codexhost"
)

// Proc is a running host process.
type Proc interface {
	// Kill ends the process and its children.
	Kill()
	// Done is closed when the process ended.
	Done() <-chan struct{}
}

// SpawnFunc starts an app-server with an explicit environment. Tests return an in-process fake.
type SpawnFunc func(ctx context.Context, cfg HostConfig, env, args []string) (io.ReadWriteCloser, Proc, error)

// Supervisor owns at most one app-server per organization.
type Supervisor struct {
	Spawn SpawnFunc
	// IdleStop stops a host that nobody has used for this long (0 = never).
	IdleStop time.Duration
	// ClientVersion is reported to the app-server.
	ClientVersion string

	mu    sync.Mutex
	hosts map[string]*host
}

type host struct {
	client *codexhost.Client
	proc   Proc
	unlock func()
	pins   int
	timer  *time.Timer
	gen    int
}

func (s *Supervisor) init() {
	if s.hosts == nil {
		s.hosts = map[string]*host{}
	}
}

// flockNB takes the organization's host lock without waiting.
func flockNB(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHostBusy
		}
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// Client returns the organization's connected client, starting the app-server when needed. A second supervisor
// (another Orqen process) on the same Codex home gets ErrHostBusy. The caller must Pin the host while a run uses it.
func (s *Supervisor) Client(ctx context.Context, orgKey string, cfg HostConfig) (*codexhost.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if h := s.hosts[orgKey]; h != nil {
		select {
		case <-h.client.Done():
			s.stopLocked(orgKey) // the process died: start a new one
		default:
			return h.client, nil
		}
	}
	if s.Spawn == nil {
		return nil, ErrHostUnavailable
	}
	if err := EnsureHostTree(cfg.DataDir, cfg.OrgID); err != nil {
		return nil, err
	}
	unlock, err := flockNB(HostLockPath(cfg.DataDir, cfg.OrgID))
	if err != nil {
		return nil, err
	}
	env, err := BuildHostEnv(cfg.RT, cfg.Org, cfg.DataDir, cfg.Secrets)
	if err != nil {
		unlock()
		return nil, err
	}
	rwc, proc, err := s.Spawn(ctx, cfg, env, HostArgs(cfg.CatherdBin, cfg.Org, cfg))
	if err != nil {
		unlock()
		return nil, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	version := s.ClientVersion
	if version == "" {
		version = "dev"
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := codexhost.Dial(dctx, rwc, codexhost.ClientInfo{Name: "orqen", Title: "Orqen", Version: version})
	if err != nil {
		proc.Kill()
		unlock()
		return nil, fmt.Errorf("%w: %v", ErrHostUnavailable, err)
	}
	s.hosts[orgKey] = &host{client: client, proc: proc, unlock: unlock}
	return client, nil
}

// Pin marks the host as in use by a run; the idle timer stops.
func (s *Supervisor) Pin(orgKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := s.hosts[orgKey]; h != nil {
		h.pins++
		h.gen++
		if h.timer != nil {
			h.timer.Stop()
			h.timer = nil
		}
	}
}

// Unpin releases a run's hold; the host stops after IdleStop with no pin.
func (s *Supervisor) Unpin(orgKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hosts[orgKey]
	if h == nil {
		return
	}
	if h.pins > 0 {
		h.pins--
	}
	if h.pins == 0 && s.IdleStop > 0 {
		h.gen++
		gen := h.gen
		h.timer = time.AfterFunc(s.IdleStop, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if cur := s.hosts[orgKey]; cur == h && h.gen == gen && h.pins == 0 {
				s.stopLocked(orgKey)
			}
		})
	}
}

// Running reports whether an app-server is up for the organization.
func (s *Supervisor) Running(orgKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hosts[orgKey]
	if h == nil {
		return false
	}
	select {
	case <-h.client.Done():
		return false
	default:
		return true
	}
}

func (s *Supervisor) stopLocked(orgKey string) {
	h := s.hosts[orgKey]
	if h == nil {
		return
	}
	delete(s.hosts, orgKey)
	if h.timer != nil {
		h.timer.Stop()
	}
	_ = h.client.Close()
	h.proc.Kill()
	h.unlock()
}

// Stop ends the organization's app-server.
func (s *Supervisor) Stop(orgKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked(orgKey)
}

// StopAll ends every app-server (shutdown).
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.hosts {
		s.stopLocked(k)
	}
}

// ExecSpawn starts the real `codex app-server` as a child process group with the explicit environment.
func ExecSpawn(ctx context.Context, cfg HostConfig, env, args []string) (io.ReadWriteCloser, Proc, error) {
	bin := cfg.CodexBin
	if bin == "" {
		return nil, nil, errors.New("codex was not found")
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = cfg.Org.Tmp()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr tailBuffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, err
	}
	p := &execProc{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return &stdio{r: stdout, w: stdin}, p, nil
}

type stdio struct {
	r io.ReadCloser
	w io.WriteCloser
}

func (s *stdio) Read(b []byte) (int, error)  { return s.r.Read(b) }
func (s *stdio) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *stdio) Close() error                { _ = s.w.Close(); return s.r.Close() }

type execProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *execProc) Done() <-chan struct{} { return p.done }

// Kill sends SIGTERM to the process group, then SIGKILL after 5 s.
func (p *execProc) Kill() {
	select {
	case <-p.done:
		return
	default:
	}
	pgid := p.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-p.done
	}
}

// tailBuffer keeps the last 8 KiB of stderr; it is never logged unscrubbed.
type tailBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > 8192 {
		b := t.buf.Bytes()
		t.buf = *bytes.NewBuffer(append([]byte(nil), b[len(b)-8192:]...))
	}
	return len(p), nil
}
