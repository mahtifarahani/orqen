package sysprobe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"syscall"
)

// FS is the file system seam. Probes write only inside directories Orqen owns.
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	// WriteProbe creates, syncs and removes a temporary file in dir.
	WriteProbe(dir string) error
	FreeBytes(path string) (uint64, error)
	Exists(path string) bool
}
type RealFS struct{}

func (RealFS) MkdirAll(p string, perm os.FileMode) error { return os.MkdirAll(p, perm) }
func (RealFS) WriteProbe(dir string) error {
	var b [6]byte
	_, _ = rand.Read(b[:])
	p := filepath.Join(dir, ".orqen-probe-"+hex.EncodeToString(b[:]))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString("orqen preflight")
	serr := f.Sync()
	cerr := f.Close()
	rerr := os.Remove(p)
	for _, e := range []error{werr, serr, cerr, rerr} {
		if e != nil {
			return e
		}
	}
	return nil
}
func (RealFS) FreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
func (RealFS) Exists(p string) bool { _, err := os.Stat(p); return err == nil }

const lowDisk = 2 << 30

func fsChecks(_ context.Context, d Deps, in Inputs) []Check {
	specs := []struct {
		id, dir  string
		perm     os.FileMode
		required bool
		blocks   []string
	}{
		{"fs.data_dir", in.DataDir, 0o755, true, []string{"artifacts"}},
		{"fs.artifacts", in.Artifacts, 0o700, true, []string{"artifacts"}},
		{"fs.catherd_home", in.CatherdHome, 0o700, true, []string{"profile_publish"}},
		{"fs.workspaces", in.Workspaces, 0o700, false, []string{"managed_workspaces"}},
	}
	var out []Check
	for _, s := range specs {
		out = append(out, timed(func() Check {
			c := Check{ID: s.id, Group: "storage", Required: s.required, Blocks: s.blocks}
			bad := Warn
			if s.required {
				bad = Fail
			}
			if err := d.FS.MkdirAll(s.dir, s.perm); err != nil {
				c.Status, c.FixCode, c.Detail = bad, "dir_not_creatable", "cannot create "+s.dir+": "+err.Error()
				return c
			}
			if err := d.FS.WriteProbe(s.dir); err != nil {
				c.Status, c.FixCode, c.Detail = bad, "dir_not_writable", "cannot write in "+s.dir+": "+err.Error()
				return c
			}
			c.Status, c.Detail = Pass, s.dir+" is writable"
			if free, err := d.FS.FreeBytes(s.dir); err == nil && free < lowDisk {
				c.Status, c.FixCode, c.Detail = Warn, "disk_low", s.dir+" has less than 2 GiB free"
			}
			return c
		}))
	}
	return out
}
