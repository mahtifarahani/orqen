package sysprobe

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

// Runner runs one executable with a timeout and a capped output.
type Runner interface {
	Run(ctx context.Context, argv []string, env []string) (out string, exit int, err error)
}

const outputCap = 64 << 10

type capWriter struct{ buf bytes.Buffer }

func (w *capWriter) Write(p []byte) (int, error) {
	if room := outputCap - w.buf.Len(); room > 0 {
		w.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

// ExecRunner runs real subprocesses with an explicit environment and a 10 second limit.
type ExecRunner struct{ Dir string }

func (r ExecRunner) Run(ctx context.Context, argv []string, env []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = r.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	out := &capWriter{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err == nil {
		return out.buf.String(), 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out.buf.String(), ee.ExitCode(), nil
	}
	return out.buf.String(), -1, err
}

// lookPath finds an executable on a PATH list.
func lookPath(path, name string) string {
	for _, d := range splitPath(path) {
		p := d + "/" + name
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}
func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == os.PathListSeparator {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
