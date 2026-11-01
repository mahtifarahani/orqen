package codegraph

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Limits of an extracted tree.
const (
	maxTreeFile  = 32 << 20
	maxTreeTotal = 512 << 20
	maxTreeFiles = 200000
)

// ExtractTar unpacks a `git archive --format=tar` stream into dest (created if missing) and nothing else. Only
// regular files and directories are written. Absolute names, `..` segments, links of any kind, devices and names that
// would leave dest are refused, and nothing is written outside dest.
func ExtractTar(r io.Reader, dest string) error {
	if !filepath.IsAbs(dest) {
		return errors.New("codegraph: destination must be absolute")
	}
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	realDest, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("codegraph: bad archive: %w", err)
		}
		switch h.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			continue // git archive writes the commit id as a pax global header
		case tar.TypeReg, tar.TypeDir:
		default:
			return fmt.Errorf("codegraph: archive entry %q has the unsupported type %q", h.Name, string(h.Typeflag))
		}
		name := h.Name
		if name == "" || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) {
			return fmt.Errorf("codegraph: archive entry %q has an unsafe name", name)
		}
		for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
			if seg == ".." {
				return fmt.Errorf("codegraph: archive entry %q leaves the destination", name)
			}
		}
		clean := path.Clean(name)
		if clean == "." {
			continue
		}
		target := filepath.Join(realDest, filepath.FromSlash(clean))
		if target != realDest && !strings.HasPrefix(target, realDest+string(filepath.Separator)) {
			return fmt.Errorf("codegraph: archive entry %q leaves the destination", name)
		}
		if h.Typeflag == tar.TypeDir {
			if err = mkdirInside(realDest, target); err != nil {
				return err
			}
			continue
		}
		if h.Size > maxTreeFile {
			return fmt.Errorf("codegraph: archive entry %q is too large", name)
		}
		if total += h.Size; total > maxTreeTotal || files+1 > maxTreeFiles {
			return errors.New("codegraph: archive is too large")
		}
		files++
		if err = mkdirInside(realDest, filepath.Dir(target)); err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if h.Mode&0o111 != 0 {
			mode = 0o700
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		n, cerr := io.Copy(f, io.LimitReader(tr, h.Size))
		if err = f.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil || n != h.Size {
			return fmt.Errorf("codegraph: cannot write %q", name)
		}
	}
}

// mkdirInside creates dir (below root) one segment at a time and refuses to walk through a symbolic link.
func mkdirInside(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("codegraph: directory leaves the destination")
	}
	cur := root
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "." || seg == "" {
			continue
		}
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		switch {
		case os.IsNotExist(err):
			if err = os.Mkdir(cur, 0o700); err != nil {
				return err
			}
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
			return fmt.Errorf("codegraph: %q is not a plain directory", cur)
		}
	}
	return nil
}
