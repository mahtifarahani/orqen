// Package verify runs the approved project commands in a detached checkout, classifies their results against a
// baseline and detects test weakening. It is independent of the server.
package verify

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// SandboxExec is the macOS sandbox launcher.
const SandboxExec = "/usr/bin/sandbox-exec"

func quote(p string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"` }

// SandboxProfile denies outbound network (unless the command needs it) and every file write outside the checkout
// and its private temporary directory. Paths must be symlink-free.
func SandboxProfile(checkout, tmp string, network bool) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	if !network {
		b.WriteString("(deny network-outbound)\n")
	}
	b.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* (subpath %s) (subpath %s) (literal \"/dev/null\") (literal \"/dev/tty\") (regex #\"^/dev/(fd/[0-9]+|std(out|err))$\"))\n", quote(checkout), quote(tmp))
	return b.String()
}

var (
	probeOnce sync.Once
	probeOK   bool
)

// SandboxAvailable reports whether sandbox-exec exists and works here.
func SandboxAvailable() bool {
	probeOnce.Do(func() {
		if _, err := os.Stat(SandboxExec); err != nil {
			return
		}
		probeOK = exec.Command(SandboxExec, "-p", "(version 1)(allow default)", "/usr/bin/true").Run() == nil
	})
	return probeOK
}

// realPath resolves symlinks (macOS /var is /private/var; the sandbox sees real paths).
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
