package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func artifactPath(root, org, digest string) (string, error) {
	if !digestPattern.MatchString(digest) || !validID("org", org) {
		return "", fmt.Errorf("invalid artifact path")
	}
	return filepath.Join(root, org, "sha256", digest[:2], digest[2:4], digest), nil
}

type Blob struct {
	SHA256 string
	Size   int64
}

func storeBlob(root, org string, reader io.Reader, maxBytes int64) (Blob, error) {
	tmpDir := filepath.Join(root, ".tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return Blob{}, err
	}
	f, err := os.CreateTemp(tmpDir, "blob-")
	if err != nil {
		return Blob{}, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return Blob{}, err
	}
	if n > maxBytes {
		return Blob{}, problem("payload_too_large")
	}
	if err = f.Sync(); err != nil {
		return Blob{}, err
	}
	if err = f.Close(); err != nil {
		return Blob{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	dest, err := artifactPath(root, org, digest)
	if err != nil {
		return Blob{}, err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return Blob{}, err
	}
	if _, err = os.Stat(dest); os.IsNotExist(err) {
		if err = os.Rename(f.Name(), dest); err != nil {
			return Blob{}, err
		}
	} else if err != nil {
		return Blob{}, err
	}
	dir, err := os.Open(filepath.Dir(dest))
	if err != nil {
		return Blob{}, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return Blob{}, err
	}
	return Blob{digest, n}, nil
}
func (a *App) recordArtifact(ctx context.Context, org string, blob Blob, media, kind string, source Subject) (ArtifactDoc, error) {
	var doc ArtifactDoc
	err := a.DB.Coll("artifacts").FindOneAndUpdate(ctx, bson.M{"org_id": org, "sha256": blob.SHA256}, bson.M{"$setOnInsert": bson.M{"_id": newID("art"), "size": blob.Size, "media_type": media, "kind": kind, "source": source, "created_at": time.Now().UTC()}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&doc)
	return doc, err
}
func (a *App) artifact(r *http.Request) (ArtifactDoc, error) {
	return findOne[ArtifactDoc](r.Context(), a.DB, "artifacts", bson.M{"_id": r.PathValue("artId"), "org_id": r.PathValue("orgId")})
}
func (a *App) getArtifact(w http.ResponseWriter, r *http.Request) error {
	d, err := a.artifact(r)
	if err != nil {
		return err
	}
	writeJSON(w, 200, d.DTO())
	return nil
}
func (a *App) getArtifactContent(w http.ResponseWriter, r *http.Request) error {
	d, err := a.artifact(r)
	if err != nil {
		return err
	}
	path, err := artifactPath(filepath.Join(a.Config.DataDir, "artifacts"), d.OrgID, d.Sha256)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("artifact blob unavailable: %w", err)
	}
	defer f.Close()
	media := d.MediaType
	if media == "" {
		media = "application/octet-stream"
	}
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Length", fmt.Sprint(max(d.Size, 0)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if media != "application/json" {
		w.Header().Set("Content-Disposition", "attachment")
	}
	_, _ = io.Copy(w, f)
	return nil
}

// readBlob returns a stored blob's bytes.
func readBlob(root, org, digest string) ([]byte, error) {
	p, err := artifactPath(root, org, digest)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}
func ensureDir(p string) error { return os.MkdirAll(p, 0o700) }
