package server

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const SessionTTL = 30 * 24 * time.Hour

func (d *DB) OwnerExists(ctx context.Context) (bool, error) {
	n, err := d.Coll("owners").CountDocuments(ctx, bson.M{"_id": "owner"})
	return n > 0, err
}
func (d *DB) IssueToken(ctx context.Context, purpose string) (string, error) {
	now := time.Now().UTC()
	ttl := 15 * time.Minute
	if purpose == "bootstrap" {
		ttl = time.Hour
		if _, err := d.Coll("one_time_tokens").UpdateMany(ctx, bson.M{"purpose": "bootstrap", "used_at": nil}, bson.M{"$set": bson.M{"used_at": now}}); err != nil {
			return "", err
		}
	}
	raw := randomToken()
	_, err := d.Coll("one_time_tokens").InsertOne(ctx, OneTimeTokenDoc{ID: newID("tok"), TokenHash: sha256Hex(raw), Purpose: purpose, ExpiresAt: now.Add(ttl)})
	return raw, err
}
func (a *App) EnsureBootstrap(ctx context.Context) error {
	path := filepath.Join(a.Config.DataDir, "bootstrap-url.txt")
	exists, err := a.DB.OwnerExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		_ = os.Remove(path)
		return nil
	}
	raw, err := a.DB.IssueToken(ctx, "bootstrap")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(a.Config.DataDir, 0700); err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/bootstrap#token=%s", a.Config.Port, raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if _, err = f.WriteString(url + "\n"); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	fmt.Printf("Open this link once to claim Orqen (valid for 1 hour):\n  %s\n", url)
	return nil
}
func (a *App) loadSession(ctx context.Context, raw string) (*SessionDoc, error) {
	doc, err := findOne[SessionDoc](ctx, a.DB, "sessions", bson.M{"token_hash": sha256Hex(raw), "expires_at": bson.M{"$gt": time.Now().UTC()}})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if time.Since(doc.LastSeenAt) > 5*time.Minute {
		if _, err = a.DB.Coll("sessions").UpdateOne(ctx, bson.M{"_id": doc.ID}, bson.M{"$set": bson.M{"last_seen_at": time.Now().UTC()}}); err != nil {
			return nil, err
		}
	}
	return &doc, nil
}
func (a *App) authStatus(w http.ResponseWriter, r *http.Request) error {
	exists, err := a.DB.OwnerExists(r.Context())
	if err != nil {
		return err
	}
	var csrf any
	if s := currentSession(r); s != nil {
		csrf = s.CsrfToken
	}
	writeJSON(w, 200, bson.M{"bootstrapped": exists, "authenticated": currentSession(r) != nil, "csrf_token": csrf})
	return nil
}
func (a *App) consumeAuth(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Token *string `json:"token"`
	}
	if err := decodeJSON(w, r, &req, false); err != nil {
		return err
	}
	if req.Token == nil {
		return validation("body", "missing field token")
	}
	purpose := "login"
	if r.URL.Path == "/api/v1/auth/bootstrap" {
		purpose = "bootstrap"
	}
	var raw, csrf string
	err := a.DB.Txn(r.Context(), func(ctx context.Context) error {
		exists, err := a.DB.OwnerExists(ctx)
		if err != nil {
			return err
		}
		if purpose == "bootstrap" && exists {
			return problem("already_bootstrapped")
		}
		if purpose == "login" && !exists {
			return problem("invalid_token")
		}
		now := time.Now().UTC()
		res, err := a.DB.Coll("one_time_tokens").UpdateOne(ctx, bson.M{"token_hash": sha256Hex(*req.Token), "purpose": purpose, "used_at": nil, "expires_at": bson.M{"$gt": now}}, bson.M{"$set": bson.M{"used_at": now}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return problem("invalid_token")
		}
		if purpose == "bootstrap" {
			if _, err = a.DB.Coll("owners").InsertOne(ctx, bson.M{"_id": "owner", "created_at": now}); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return problem("already_bootstrapped")
				}
				return err
			}
		}
		raw = randomToken()
		csrf = randomToken()
		if _, err = a.DB.Coll("sessions").InsertOne(ctx, SessionDoc{ID: newID("sess"), TokenHash: sha256Hex(raw), CsrfToken: csrf, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(SessionTTL)}); err != nil {
			return err
		}
		if purpose == "bootstrap" {
			return a.AppendEvent(ctx, "", "auth.bootstrapped", Subject{"owner", "owner"}, bson.M{}, nil, nil, false)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if purpose == "bootstrap" {
		_ = os.Remove(filepath.Join(a.Config.DataDir, "bootstrap-url.txt"))
		a.Committed()
	}
	a.setSessionCookie(w, raw, int(SessionTTL.Seconds()))
	writeJSON(w, 200, bson.M{"csrf_token": csrf})
	return nil
}
func (a *App) setSessionCookie(w http.ResponseWriter, raw string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.Config.CookieName(), Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.DB.Coll("sessions").DeleteOne(r.Context(), bson.M{"_id": currentSession(r).ID}); err != nil {
		return err
	}
	a.setSessionCookie(w, "", -1)
	w.WriteHeader(204)
	return nil
}
