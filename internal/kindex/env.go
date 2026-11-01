package kindex

import (
	"os"
	"path/filepath"
	"strings"
)

// OrgRoot is the only tree the adapter may touch for an org: <data>/wiki/<org>.
func OrgRoot(dataDir, orgID string) string { return filepath.Join(dataDir, "wiki", orgID) }

// VaultDir is the default vault directory of an org.
func VaultDir(dataDir, orgID string) string { return filepath.Join(OrgRoot(dataDir, orgID), "vault") }

// Env builds the explicit child environment: nothing is inherited and no credential is passed.
func Env(dataDir, orgID, childPath string) []string {
	root := OrgRoot(dataDir, orgID)
	return []string{
		"HOME=" + filepath.Join(root, "home"),
		"OPENCLAW_HOME=" + filepath.Join(root, "state"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"PATH=" + childPath,
		"LANG=C.UTF-8",
	}
}

// resolveVault validates the vault against the org root (after symlink resolution) and creates the per-org
// directories. A vault outside <data>/wiki/<org> is refused.
func resolveVault(dataDir string, v Vault) (root, vault string, err error) {
	if v.OrgID == "" || strings.ContainsAny(v.OrgID, `/\`) || strings.Contains(v.OrgID, "..") || !filepath.IsAbs(v.Dir) {
		return "", "", errf(KindBadVault, "", "invalid vault %q for org %q", v.Dir, v.OrgID)
	}
	root = OrgRoot(dataDir, v.OrgID)
	for _, d := range []string{"home", "state", "tmp", "vault"} {
		if err = os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return "", "", errf(KindUnavailable, "", "cannot create %s: %v", d, err)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", errf(KindBadVault, "", "%v", err)
	}
	if err = os.MkdirAll(v.Dir, 0o700); err != nil {
		return "", "", errf(KindBadVault, "", "%v", err)
	}
	realVault, err := filepath.EvalSymlinks(v.Dir)
	if err != nil {
		return "", "", errf(KindBadVault, "", "%v", err)
	}
	if realVault != realRoot && !strings.HasPrefix(realVault, realRoot+string(filepath.Separator)) {
		return "", "", errf(KindBadVault, "", "vault %q is outside the org directory", v.Dir)
	}
	return realRoot, realVault, nil
}

// InsideOrg reports whether path (resolved) lies inside the org root; used for input directories and files.
func InsideOrg(dataDir, orgID, path string) bool {
	rootReal, err := filepath.EvalSymlinks(OrgRoot(dataDir, orgID))
	if err != nil {
		return false
	}
	p, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return p == rootReal || strings.HasPrefix(p, rootReal+string(filepath.Separator))
}
