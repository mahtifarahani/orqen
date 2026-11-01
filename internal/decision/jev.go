package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// DefaultBase is Jev's API origin.
const DefaultBase = "https://api.typesafe.ai"

// Options configures the Jev client. Zero values are the production policy: 10 s per attempt, 25 s overall, two
// retries on 408, 429, 5xx, network errors and timeouts, Retry-After honoured up to 10 s.
type Options struct {
	Base     string
	HTTP     *http.Client
	Attempt  time.Duration
	Deadline time.Duration
	Retries  int // 0 means 2; negative means none
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
}

type jev struct{ o Options }

// NewJev returns the real client. It reads no key itself: the caller passes it to Ask.
func NewJev(o Options) Port {
	if o.Base == "" {
		o.Base = DefaultBase
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	if o.Attempt <= 0 {
		o.Attempt = 10 * time.Second
	}
	if o.Deadline <= 0 {
		o.Deadline = 25 * time.Second
	}
	if o.Retries == 0 {
		o.Retries = 2
	} else if o.Retries < 0 {
		o.Retries = 0
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &jev{o}
}

const retryAfterMax = 10 * time.Second

func retryable(status int) bool { return status == 408 || status == 429 || status >= 500 }

func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	if v := strings.TrimSpace(h.Get("Retry-After-Ms")); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			return min(max(time.Duration(ms*float64(time.Millisecond)), 0), retryAfterMax), true
		}
	}
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		return min(max(time.Duration(secs*float64(time.Second)), 0), retryAfterMax), true
	}
	if t, err := http.ParseTime(v); err == nil {
		return min(max(t.Sub(now), 0), retryAfterMax), true
	}
	return 0, false
}
func backoff(retry int) time.Duration {
	return time.Duration(float64(500*time.Millisecond) * float64(int(1)<<(retry-1)) * (0.75 + rand.Float64()*0.5))
}

type wireReply struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   *struct {
		In  int `json:"input_tokens"`
		Out int `json:"output_tokens"`
	} `json:"usage"`
}
type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         *float64           `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Noul          *float64           `json:"noul"`
}

// parseReply validates every question: answered in its own type and inside its options.
func parseReply(qs map[string]Question, body []byte) (Response, error) {
	var r wireReply
	if json.Unmarshal(body, &r) != nil || r.Model == "" {
		return Response{}, ErrInvalid
	}
	out := Response{Model: r.Model, Answers: map[string]Answer{}}
	if r.Usage != nil {
		out.InputTokens, out.OutputTokens = r.Usage.In, r.Usage.Out
	}
	for id, q := range qs {
		raw, ok := r.Answers[id]
		if !ok {
			return Response{}, ErrInvalid
		}
		var a wireAnswer
		if json.Unmarshal(raw, &a) != nil || a.Type != q.Type {
			return Response{}, ErrInvalid
		}
		ans := Answer{Type: a.Type, Probabilities: a.Probabilities, Confidence: a.Confidence}
		switch q.Type {
		case "choice":
			if _, ok := q.Choice[a.Choice]; !ok {
				return Response{}, ErrInvalid
			}
			for opt, p := range a.Probabilities {
				if _, ok := q.Choice[opt]; !ok || p < 0 || p > 1 {
					return Response{}, ErrInvalid
				}
			}
			ans.Choice = a.Choice
		case "score":
			if a.Score == nil {
				return Response{}, ErrInvalid
			}
			for level := range a.Probabilities {
				n, err := strconv.Atoi(level)
				if err != nil || n < 0 || n >= len(q.Score) {
					return Response{}, ErrInvalid
				}
			}
			ans.Score = *a.Score
		case "noul":
			if a.Noul == nil || *a.Noul < 0 || *a.Noul > 1 {
				return Response{}, ErrInvalid
			}
			ans.Noul = *a.Noul
		default:
			return Response{}, ErrInvalid
		}
		out.Answers[id] = ans
	}
	return out, nil
}

// Ask posts the questions. The key is only placed in the Authorization header; every returned error is a sentinel.
func (j *jev) Ask(ctx context.Context, r Request, key string) (Response, error) {
	if key == "" {
		return Response{}, ErrNoKey
	}
	payload, err := json.Marshal(map[string]any{"model": r.Model, "state": r.State, "questions": r.Questions})
	if err != nil {
		return Response{}, err
	}
	start := j.o.Now()
	deadline := start.Add(j.o.Deadline)
	url := strings.TrimRight(j.o.Base, "/") + "/v1/systemone"
	for attempt := 1; ; attempt++ {
		left := deadline.Sub(j.o.Now())
		if left <= 0 {
			return Response{}, ErrTimeout
		}
		status, body, header, aerr := j.once(ctx, url, key, payload, attempt, min(j.o.Attempt, left))
		last := ErrUnavailable
		switch {
		case aerr != nil:
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			if errors.Is(aerr, context.DeadlineExceeded) {
				last = ErrTimeout
			}
		case status >= 200 && status < 300:
			resp, perr := parseReply(r.Questions, body)
			if perr != nil {
				return Response{}, perr
			}
			resp.RequestID = header.Get("x-typesafe-request-id")
			resp.LatencyMS = j.o.Now().Sub(start).Milliseconds()
			resp.Attempts = attempt
			return resp, nil
		case status == 401 || status == 403:
			return Response{}, ErrRefused
		case !retryable(status):
			return Response{}, ErrInvalid // the request was rejected (validation); retrying cannot help
		}
		if attempt > j.o.Retries {
			return Response{}, last
		}
		pause := backoff(attempt)
		if header != nil {
			if d, ok := retryAfter(header, j.o.Now()); ok {
				pause = d
			}
		}
		if !j.o.Now().Add(pause).Before(deadline) {
			return Response{}, ErrTimeout
		}
		if err := j.o.Sleep(ctx, pause); err != nil {
			return Response{}, err
		}
	}
}

// once performs one attempt with its own timeout.
func (j *jev) once(ctx context.Context, url, key string, payload []byte, attempt int, timeout time.Duration) (int, []byte, http.Header, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, nil, errors.New("decision: bad request")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	if attempt > 1 {
		req.Header.Set("x-typesafe-retry-count", strconv.Itoa(attempt-1))
	}
	resp, err := j.o.HTTP.Do(req)
	if err != nil {
		if errors.Is(actx.Err(), context.DeadlineExceeded) {
			return 0, nil, nil, context.DeadlineExceeded
		}
		return 0, nil, nil, errors.New("network error")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if errors.Is(actx.Err(), context.DeadlineExceeded) {
			return 0, nil, nil, context.DeadlineExceeded
		}
		return 0, nil, nil, errors.New("network error")
	}
	return resp.StatusCode, data, resp.Header, nil
}
