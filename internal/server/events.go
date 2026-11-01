package server

import (
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, word := range []string{"secret", "token", "password", "authorization", "cookie"} {
		if strings.Contains(k, word) {
			return true
		}
	}
	for {
		_, tail, ok := strings.Cut(k, "credential")
		if !ok {
			return false
		}
		if !strings.HasPrefix(tail, "_ref") {
			return true
		}
		k = tail
	}
}
func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		r := map[string]any{}
		for k, v := range x {
			if sensitiveKey(k) {
				r[k] = "[REDACTED]"
			} else {
				r[k] = redact(v)
			}
		}
		return r
	case bson.M:
		return redact(map[string]any(x))
	case []any:
		r := make([]any, len(x))
		for i, v := range x {
			r[i] = redact(v)
		}
		return r
	default:
		return v
	}
}
func toMap(v any) (bson.M, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(b, &m)
	return bson.M(m), err
}
func (a *App) AppendEvent(ctx context.Context, org, kind string, subject Subject, payload any, revision *int64, op *string, system bool) error {
	var counter struct {
		Value int64 `bson:"seq"`
	}
	if err := a.DB.Coll("counters").FindOneAndUpdate(ctx, bson.M{"_id": "events"}, bson.M{"$inc": bson.M{"seq": int64(1)}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&counter); err != nil {
		return err
	}
	m, err := toMap(payload)
	if err != nil {
		return err
	}
	if m == nil {
		m = bson.M{}
	}
	actor := "owner"
	if system {
		actor = "system"
	}
	var orgID *string
	if org != "" {
		orgID = &org
	}
	ev := EventDoc{ID: bson.NewObjectID(), Seq: counter.Value, OrgID: orgID, Type: kind, Actor: actor, Subject: subject, Revision: revision, OperationID: op, Payload: bson.M(redact(m).(map[string]any)), CreatedAt: time.Now().UTC()}
	_, err = a.DB.Coll("events").InsertOne(ctx, ev)
	return err
}
func (a *App) fetchEvents(ctx context.Context, org string, after int64, limit int64) ([]EventDoc, error) {
	return findMany[EventDoc](ctx, a.DB, "events", bson.M{"org_id": org, "seq": bson.M{"$gt": after}}, bson.D{{Key: "seq", Value: 1}}, limit)
}
func queryInt(r *http.Request, key string, fallback int64) (int64, error) {
	raw, ok := r.URL.Query()[key]
	if !ok {
		return fallback, nil
	}
	if len(raw) != 1 {
		return 0, validation("query", "duplicate parameter: "+key)
	}
	n, err := strconv.ParseInt(raw[0], 10, 64)
	if err != nil {
		return 0, validation("query", "invalid integer: "+key)
	}
	return n, nil
}
func (a *App) listEvents(w http.ResponseWriter, r *http.Request) error {
	after, err := queryInt(r, "after", 0)
	if err != nil {
		return err
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		return err
	}
	if limit < 1 || limit > 500 {
		return validation("limit", "must be between 1 and 500")
	}
	after = max(after, 0)
	docs, err := a.fetchEvents(r.Context(), r.PathValue("orgId"), after, limit)
	if err != nil {
		return err
	}
	items := []bson.M{}
	last := after
	for _, d := range docs {
		items = append(items, d.DTO())
		last = d.Seq
	}
	writeJSON(w, 200, bson.M{"items": items, "last_seq": last})
	return nil
}
func (a *App) streamEvents(w http.ResponseWriter, r *http.Request) error {
	after, err := queryInt(r, "after", 0)
	if err != nil {
		return err
	}
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	after = max(after, last, 0)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	poll := time.NewTicker(2 * time.Second)
	defer poll.Stop()
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		wake := a.eventSignal()
		docs, e := a.fetchEvents(r.Context(), r.PathValue("orgId"), after, 500)
		if e == nil {
			for _, d := range docs {
				b, e := json.Marshal(d.DTO())
				if e != nil {
					continue
				}
				if _, e = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", d.Seq, b); e != nil {
					return nil
				}
				flusher.Flush()
				after = d.Seq
			}
			if len(docs) == 500 {
				continue
			}
		} else {
			slog.Warn("event stream query failed", "error", e)
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-a.ctx.Done():
			return nil
		case <-wake:
		case <-poll.C:
		case <-keep.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}
