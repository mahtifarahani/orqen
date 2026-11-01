package kindex

import "orqen/internal/verify"

// Sandbox modes (policy knowledge.wiki.sandbox).
const (
	SandboxEnforce    = "enforce"
	SandboxBestEffort = "best_effort"
)

// sandboxProfile denies outbound network and every file write outside the org root and its temp directory.
func sandboxProfile(root, tmp string) string { return verify.SandboxProfile(root, tmp, false) }

// SandboxAvailable reports whether the macOS sandbox launcher works here.
func SandboxAvailable() bool { return verify.SandboxAvailable() }
