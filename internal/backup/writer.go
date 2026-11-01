package backup

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"
)

// Failure codes.
const (
	CodeSecretDetected  = "backup_secret_detected"
	CodeArtifactMissing = "artifact_missing"
	CodeDirUnwritable   = "backup_dir_unwritable"
)

// Error carries a stable code and the location (collection/_id or artifact id), never a value.
type Error struct {
	Code     string
	Location string
	Err      error
}

func (e *Error) Error() string {
	s := "backup: " + e.Code
	if e.Location != "" {
		s += " at " + e.Location
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}
func (e *Error) Unwrap() error { return e.Err }

// ArtifactPatternMode values (backup.on_artifact_pattern).
const (
	PatternFail            = "fail"
	PatternExcludeArtifact = "exclude_artifact"
)

// Writer builds one backup directory. Everything is written below <dir>/.partial-<id>/ and renamed to the final name
// by Finalize; Abort removes the partial directory.
type Writer struct {
	fs       FS
	dir      string
	partial  string
	name     string
	scanner  *Scanner
	mode     string
	cols     []CollectionEntry
	blobs    map[string]int64 // "org/digest" -> bytes
	excluded []ExcludedArtifact
	done     bool
}

// NewWriter creates the partial directory. name is the final directory name (NewName); mode is backup.on_artifact_pattern.
func NewWriter(fs FS, dir, name, mode string, scanner *Scanner) (*Writer, error) {
	if !NamePattern.MatchString(name) {
		return nil, fmt.Errorf("backup: invalid name %q", name)
	}
	if mode == "" {
		mode = PatternFail
	}
	if scanner == nil {
		scanner = NewScanner(nil)
	}
	w := &Writer{fs: fs, dir: dir, name: name, partial: filepath.Join(dir, PartialPrefix+name), scanner: scanner, mode: mode, blobs: map[string]int64{}}
	if err := fs.MkdirAll(filepath.Join(w.partial, DBDir), 0o700); err != nil {
		return nil, &Error{Code: CodeDirUnwritable, Err: err}
	}
	if err := fs.MkdirAll(filepath.Join(w.partial, ArtifactsDir), 0o700); err != nil {
		return nil, &Error{Code: CodeDirUnwritable, Err: err}
	}
	return w, nil
}

// Name is the final directory name.
func (w *Writer) Name() string { return w.name }

type shaWriter struct {
	w io.Writer
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (s *shaWriter) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	s.h.Write(p[:n])
	s.n += int64(n)
	return n, err
}

// DocIter yields canonical Extended JSON lines (without the newline) with the id used for error locations. It returns
// ok=false at the end.
type DocIter func() (id string, line []byte, ok bool, err error)

// WriteCollection writes db/<name>.jsonl.gz from the iterator. Every line is scanned before it is compressed; a hit
// fails the backup with backup_secret_detected at name/<id>.
func (w *Writer) WriteCollection(name string, next DocIter) (CollectionEntry, error) {
	if !IsIncluded(name) {
		return CollectionEntry{}, fmt.Errorf("backup: collection %q is not included", name)
	}
	rel := CollectionFile(name)
	f, err := w.fs.Create(filepath.Join(w.partial, filepath.FromSlash(rel)))
	if err != nil {
		return CollectionEntry{}, &Error{Code: CodeDirUnwritable, Err: err}
	}
	sw := &shaWriter{w: f, h: sha256.New()}
	zw := gzip.NewWriter(sw)
	bw := bufio.NewWriter(zw)
	var docs int64
	fail := func(e error) (CollectionEntry, error) { _ = zw.Close(); _ = f.Close(); return CollectionEntry{}, e }
	for {
		id, line, ok, err := next()
		if err != nil {
			return fail(err)
		}
		if !ok {
			break
		}
		if hits := w.scanner.Scan(line); len(hits) > 0 {
			return fail(&Error{Code: CodeSecretDetected, Location: name + "/" + id, Err: &SecretError{Location: name + "/" + id, Hit: hits[0]}})
		}
		bw.Write(line)
		bw.WriteByte('\n')
		docs++
	}
	if err = bw.Flush(); err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return CollectionEntry{}, &Error{Code: CodeDirUnwritable, Err: err}
	}
	e := CollectionEntry{Name: name, Docs: docs, File: rel, SHA256: hex.EncodeToString(sw.h.Sum(nil)), Bytes: sw.n}
	w.cols = append(w.cols, e)
	return e, nil
}

// MaxBlob is the largest artifact blob the backup will hold in memory to scan it.
const MaxBlob = 512 << 20

// CopyBlob copies one artifact blob after checking that its content hashes to digest and scanning it. A known credential
// value fails the backup. A pattern hit fails it too, or (mode exclude_artifact) leaves the blob out and records it.
// artifactID is the id used in errors and the manifest. It returns whether the blob was written.
func (w *Writer) CopyBlob(org, digest, artifactID string, r io.Reader) (bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBlob+1))
	if err != nil {
		return false, &Error{Code: CodeArtifactMissing, Location: artifactID, Err: err}
	}
	if len(data) > MaxBlob {
		return false, &Error{Code: CodeArtifactMissing, Location: artifactID, Err: errors.New("artifact too large to scan")}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return false, &Error{Code: CodeArtifactMissing, Location: artifactID, Err: errors.New("blob does not match its digest")}
	}
	if hits := w.scanner.Scan(data); len(hits) > 0 {
		h := hits[0]
		if h.Kind == HitKnownValue || w.mode != PatternExcludeArtifact {
			return false, &Error{Code: CodeSecretDetected, Location: artifactID, Err: &SecretError{Location: artifactID, Hit: h}}
		}
		w.excluded = append(w.excluded, ExcludedArtifact{ArtifactID: artifactID, PatternCode: h.Code})
		return false, nil
	}
	rel := BlobPath(org, digest)
	p := filepath.Join(w.partial, filepath.FromSlash(rel))
	if err = w.fs.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return false, &Error{Code: CodeDirUnwritable, Err: err}
	}
	f, err := w.fs.Create(p)
	if err != nil {
		return false, &Error{Code: CodeDirUnwritable, Err: err}
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, &Error{Code: CodeDirUnwritable, Err: err}
	}
	w.blobs[org+"/"+digest] = int64(len(data))
	return true, nil
}

// ListSHA is the digest of the sorted "org/digest" lines of the copied blobs.
func ListSHA(keys []string) string {
	sorted := append([]string{}, keys...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, k := range sorted {
		h.Write([]byte(k + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Finalize completes the manifest from what was written, writes manifest.json and MANIFEST.sha256, syncs everything and
// renames the partial directory to its final name. The caller fills the header fields of m; Collections, Artifacts,
// Excluded and the secret-scan summary are set here.
func (w *Writer) Finalize(m Manifest, now time.Time) (string, error) {
	if w.done {
		return "", errors.New("backup: already finalized")
	}
	m.Format, m.FormatVersion = FormatName, FormatVersion
	m.CreatedAt = now.UTC()
	sort.Slice(w.cols, func(i, j int) bool { return w.cols[i].Name < w.cols[j].Name })
	m.Collections = w.cols
	keys := make([]string, 0, len(w.blobs))
	var total int64
	for k, n := range w.blobs {
		keys = append(keys, k)
		total += n
	}
	m.Artifacts = ArtifactsEntry{Count: len(keys), Bytes: total, ListSHA256: ListSHA(keys)}
	m.Excluded = Excluded{Collections: ExcludedCollections(), DataDirs: append([]string{}, ExcludedDataDirs...)}
	excl := append([]ExcludedArtifact{}, w.excluded...)
	sort.Slice(excl, func(i, j int) bool { return excl[i].ArtifactID < excl[j].ArtifactID })
	m.SecretScan = SecretScan{KnownValuesChecked: w.scanner.KnownChecked(), PatternsVersion: PatternsVersion, ExcludedArtifacts: excl}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	if err = w.writeFile(ManifestFile, raw); err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	if err = w.writeFile(ManifestSum, []byte(hex.EncodeToString(sum[:])+"  "+ManifestFile+"\n")); err != nil {
		return "", err
	}
	if err = w.fs.SyncDir(w.partial); err != nil {
		return "", &Error{Code: CodeDirUnwritable, Err: err}
	}
	final := filepath.Join(w.dir, w.name)
	if err = w.fs.Rename(w.partial, final); err != nil {
		return "", &Error{Code: CodeDirUnwritable, Err: err}
	}
	_ = w.fs.SyncDir(w.dir)
	w.done = true
	return w.name, nil
}

func (w *Writer) writeFile(rel string, data []byte) error {
	f, err := w.fs.Create(filepath.Join(w.partial, rel))
	if err != nil {
		return &Error{Code: CodeDirUnwritable, Err: err}
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return &Error{Code: CodeDirUnwritable, Err: err}
	}
	return nil
}

// Abort removes the partial directory. It is safe to call after Finalize.
func (w *Writer) Abort() {
	if !w.done {
		_ = w.fs.RemoveAll(w.partial)
	}
}
