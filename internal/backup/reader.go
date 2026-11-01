package backup

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// Failure codes of verification and restore.
const (
	CodeIntegrityFailed = "restore_integrity_failed"
	CodeSchemaTooNew    = "restore_schema_too_new"
)

// IntegrityError names the file that failed.
type IntegrityError struct{ File string }

func (e *IntegrityError) Error() string { return CodeIntegrityFailed + ": " + e.File }

// Backup is an opened backup directory.
type Backup struct {
	fs       FS
	Dir      string
	Name     string
	Manifest Manifest
}

// Open reads the manifest of a backup directory and checks it against MANIFEST.sha256. It does not read the data files.
func Open(fs FS, dir string) (*Backup, error) {
	raw, err := fs.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, &IntegrityError{File: ManifestFile}
	}
	sumRaw, err := fs.ReadFile(filepath.Join(dir, ManifestSum))
	if err != nil {
		return nil, &IntegrityError{File: ManifestSum}
	}
	sum := sha256.Sum256(raw)
	if fields := strings.Fields(string(sumRaw)); len(fields) == 0 || fields[0] != hex.EncodeToString(sum[:]) {
		return nil, &IntegrityError{File: ManifestFile}
	}
	var m Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return nil, &IntegrityError{File: ManifestFile}
	}
	if m.Format != FormatName || m.FormatVersion != FormatVersion {
		return nil, fmt.Errorf("backup: unsupported format %q v%d", m.Format, m.FormatVersion)
	}
	return &Backup{fs: fs, Dir: dir, Name: filepath.Base(dir), Manifest: m}, nil
}

// FileResult is the verification result of one file.
type FileResult struct {
	File string `json:"file"`
	OK   bool   `json:"ok"`
	Docs int64  `json:"docs,omitempty"`
}

func (b *Backup) hashFile(rel string) (string, int64, error) {
	r, err := b.fs.Open(filepath.Join(b.Dir, filepath.FromSlash(rel)))
	if err != nil {
		return "", 0, err
	}
	defer r.Close()
	h := sha256.New()
	n, err := io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// VerifyAll checks every collection file (sha256 and size), every blob (the file name is its digest) and the blob list
// digest. The result lists each file; the manifest itself was checked by Open.
func (b *Backup) VerifyAll() []FileResult {
	out := []FileResult{{File: ManifestFile, OK: true}}
	for _, c := range b.Manifest.Collections {
		sum, n, err := b.hashFile(c.File)
		out = append(out, FileResult{File: c.File, OK: err == nil && sum == c.SHA256 && n == c.Bytes, Docs: c.Docs})
	}
	keys, err := b.blobKeys()
	ok := err == nil && len(keys) == b.Manifest.Artifacts.Count && ListSHA(keys) == b.Manifest.Artifacts.ListSHA256
	out = append(out, FileResult{File: ArtifactsDir + "/", OK: ok})
	for _, k := range keys {
		org, digest, _ := strings.Cut(k, "/")
		rel := BlobPath(org, digest)
		sum, _, err := b.hashFile(rel)
		out = append(out, FileResult{File: rel, OK: err == nil && sum == digest})
	}
	return out
}

// AllOK reports whether every result is ok.
func AllOK(rs []FileResult) bool {
	for _, r := range rs {
		if !r.OK {
			return false
		}
	}
	return true
}

// FirstFailure returns the first failed file.
func FirstFailure(rs []FileResult) (string, bool) {
	for _, r := range rs {
		if !r.OK {
			return r.File, true
		}
	}
	return "", false
}

// blobKeys lists "org/digest" for every blob file below artifacts/.
func (b *Backup) blobKeys() ([]string, error) {
	var keys []string
	root := filepath.Join(b.Dir, ArtifactsDir)
	orgs, err := b.fs.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, o := range orgs {
		if !o.IsDir() {
			return nil, fmt.Errorf("unexpected file %s", o.Name())
		}
		base := filepath.Join(root, o.Name(), "sha256")
		l1, err := b.fs.ReadDir(base)
		if err != nil {
			continue // an org without blobs
		}
		for _, a := range l1 {
			l2, err := b.fs.ReadDir(filepath.Join(base, a.Name()))
			if err != nil {
				return nil, err
			}
			for _, c := range l2 {
				files, err := b.fs.ReadDir(filepath.Join(base, a.Name(), c.Name()))
				if err != nil {
					return nil, err
				}
				for _, f := range files {
					keys = append(keys, o.Name()+"/"+f.Name())
				}
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Blobs lists the blobs ("org/digest" pairs split) of the backup.
func (b *Backup) Blobs() ([][2]string, error) {
	keys, err := b.blobKeys()
	if err != nil {
		return nil, err
	}
	out := make([][2]string, len(keys))
	for i, k := range keys {
		org, digest, _ := strings.Cut(k, "/")
		out[i] = [2]string{org, digest}
	}
	return out, nil
}

// OpenBlob opens a blob for reading.
func (b *Backup) OpenBlob(org, digest string) (io.ReadCloser, error) {
	return b.fs.Open(filepath.Join(b.Dir, filepath.FromSlash(BlobPath(org, digest))))
}

// ReadCollection streams the lines of a collection file to fn. The file's hash is checked first, so a flipped byte fails
// before any line is delivered.
func (b *Backup) ReadCollection(name string, fn func(line []byte) error) error {
	var entry *CollectionEntry
	for i := range b.Manifest.Collections {
		if b.Manifest.Collections[i].Name == name {
			entry = &b.Manifest.Collections[i]
		}
	}
	if entry == nil {
		return fmt.Errorf("backup: collection %q is not in the manifest", name)
	}
	sum, n, err := b.hashFile(entry.File)
	if err != nil || sum != entry.SHA256 || n != entry.Bytes {
		return &IntegrityError{File: entry.File}
	}
	r, err := b.fs.Open(filepath.Join(b.Dir, filepath.FromSlash(entry.File)))
	if err != nil {
		return &IntegrityError{File: entry.File}
	}
	defer r.Close()
	zr, err := gzip.NewReader(r)
	if err != nil {
		return &IntegrityError{File: entry.File}
	}
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var count int64
	for sc.Scan() {
		count++
		if err = fn(append([]byte{}, sc.Bytes()...)); err != nil {
			return err
		}
	}
	if sc.Err() != nil || count != entry.Docs {
		return &IntegrityError{File: entry.File}
	}
	return nil
}

// ListEntry is one directory of the backup directory that looks like a backup.
type ListEntry struct {
	Name     string
	Valid    bool // the manifest and its checksum are intact
	Manifest *Manifest
}

// List returns the backups in dir, newest name first. Directories that do not match NamePattern (including
// .partial-* directories and foreign files) are ignored.
func List(fs FS, dir string) ([]ListEntry, error) {
	ents, err := fs.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ListEntry
	for _, e := range ents {
		if !e.IsDir() || !NamePattern.MatchString(e.Name()) {
			continue
		}
		le := ListEntry{Name: e.Name()}
		if b, err := Open(fs, filepath.Join(dir, e.Name())); err == nil {
			le.Valid, le.Manifest = true, &b.Manifest
		}
		out = append(out, le)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}
