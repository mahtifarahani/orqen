package backup

import (
	"io"
	"os"
)

// WFile is a file being written.
type WFile interface {
	io.Writer
	Sync() error
	Close() error
}

// FS is the file system the writer and reader use. OSFS is the real one; tests may wrap it.
type FS interface {
	MkdirAll(path string, perm os.FileMode) error
	Create(path string) (WFile, error) // mode 0600, fails if the file exists
	Open(path string) (io.ReadCloser, error)
	ReadFile(path string) ([]byte, error)
	ReadDir(path string) ([]os.DirEntry, error)
	Rename(oldpath, newpath string) error
	RemoveAll(path string) error
	SyncDir(path string) error
}

// OSFS is the operating-system file system.
type OSFS struct{}

func (OSFS) MkdirAll(p string, perm os.FileMode) error { return os.MkdirAll(p, perm) }
func (OSFS) Create(p string) (WFile, error) {
	return os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
}
func (OSFS) Open(p string) (io.ReadCloser, error)    { return os.Open(p) }
func (OSFS) ReadFile(p string) ([]byte, error)       { return os.ReadFile(p) }
func (OSFS) ReadDir(p string) ([]os.DirEntry, error) { return os.ReadDir(p) }
func (OSFS) Rename(a, b string) error                { return os.Rename(a, b) }
func (OSFS) RemoveAll(p string) error                { return os.RemoveAll(p) }
func (OSFS) SyncDir(p string) error {
	d, err := os.Open(p)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
