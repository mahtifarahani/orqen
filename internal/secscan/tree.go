package secscan

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Manifest maps every file path below dir (slash-separated, relative) to its sha256. A symbolic link is recorded as
// the digest of "symlink:" plus its target, so a link that appears or changes shows in the diff.
func Manifest(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		h := sha256.New()
		if info.Mode()&os.ModeSymlink != 0 {
			t, _ := os.Readlink(p)
			h.Write([]byte("symlink:" + t))
		} else if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(h, f)
			f.Close()
			if cerr != nil {
				return cerr
			}
		} else {
			h.Write([]byte("special:" + info.Mode().String()))
		}
		out[rel] = hex.EncodeToString(h.Sum(nil))
		return nil
	})
	return out, err
}

// TreeDiff lists what changed between two manifests; each list is sorted.
type TreeDiff struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// Empty reports whether nothing changed.
func (d TreeDiff) Empty() bool { return len(d.Added)+len(d.Removed)+len(d.Changed) == 0 }

// Diff compares the manifest taken before a scan with the one taken after.
func Diff(before, after map[string]string) TreeDiff {
	d := TreeDiff{Added: []string{}, Removed: []string{}, Changed: []string{}}
	for p, h := range after {
		old, ok := before[p]
		switch {
		case !ok:
			d.Added = append(d.Added, p)
		case old != h:
			d.Changed = append(d.Changed, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			d.Removed = append(d.Removed, p)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}
