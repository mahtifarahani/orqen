package extensions

import (
	"fmt"
	"path/filepath"
	"strings"

	"orqen/internal/verify"
)

// Sandbox modes (policy extensions.sandbox).
const (
	SandboxEnforce    = "enforce"
	SandboxBestEffort = "best_effort"
)

// SandboxAvailable reports whether the macOS sandbox launcher works here.
func SandboxAvailable() bool { return verify.SandboxAvailable() }

func quote(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(p) + `"`
}

const devAllow = `(literal "/dev/null") (literal "/dev/tty") (regex #"^/dev/(fd/[0-9]+|std(out|err))$")`

func writeAllow(dir string, extra []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "(allow file-write* (subpath %s)", quote(dir))
	for _, e := range extra {
		fmt.Fprintf(&b, " (subpath %s)", quote(e))
	}
	b.WriteString(" " + devAllow + ")\n")
	return b.String()
}

// DenyNetworkWriteUnder denies all network access and every file write outside dir (and the extra paths).
func DenyNetworkWriteUnder(dir string, extra ...string) string {
	return "(version 1)\n(allow default)\n(deny network*)\n(deny file-write*)\n" + writeAllow(dir, extra)
}

// WriteUnder denies every file write outside dir (and the extra paths) and leaves the network alone. Strix needs the
// model provider, so the UI labels the network as not restricted.
func WriteUnder(dir string, extra ...string) string {
	return "(version 1)\n(allow default)\n(deny file-write*)\n" + writeAllow(dir, extra)
}
