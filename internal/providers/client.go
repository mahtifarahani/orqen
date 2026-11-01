package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxJSONBytes = 8 << 20
	userAgent    = "orqen"
)

// Options configures the HTTP client and, for adapters, the base URL and the per-connection concurrency gate.
type Options struct {
	BaseURL     string
	Timeout     time.Duration
	ProxyURL    string
	CustomCAPEM string
	// Acquire takes a concurrency permit and returns its release function. Nil means unlimited.
	Acquire func(context.Context) (func(), error)
	// HTTP overrides the client (tests).
	HTTP *http.Client
}

// NewClient builds the client shared by connection probes and adapters. API redirects are never followed.
func NewClient(o Options) (*http.Client, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: timeout}).DialContext, TLSHandshakeTimeout: timeout, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if o.ProxyURL != "" {
		p, err := url.Parse(o.ProxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(p)
	}
	if o.CustomCAPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(o.CustomCAPEM)) {
			return nil, fmt.Errorf("invalid custom CA")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type base struct {
	o       Options
	auth    Auth
	hc      *http.Client
	initErr error
	root    *url.URL
}

func newBase(o Options, auth Auth) base {
	b := base{o: o, auth: auth}
	if o.HTTP != nil {
		b.hc = o.HTTP
	} else {
		b.hc, b.initErr = NewClient(o)
	}
	if b.initErr == nil {
		b.root, b.initErr = url.Parse(strings.TrimRight(o.BaseURL, "/"))
	}
	return b
}

type response struct {
	status  int
	header  http.Header
	body    io.ReadCloser
	release func()
}

func (r *response) close() { _ = r.body.Close(); r.release() }

// send performs one GET. A non-2xx status becomes a provider Error; the caller closes the response.
func (b *base) send(ctx context.Context, rawURL string) (*response, error) {
	if b.initErr != nil {
		return nil, newError(KindConfig, "client_error", "the connection's HTTP settings are invalid")
	}
	release := func() {}
	if b.o.Acquire != nil {
		var err error
		if release, err = b.o.Acquire(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		release()
		return nil, newError(KindConfig, "invalid_url", "the request URL is invalid")
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if err = b.auth.Apply(req); err != nil {
		release()
		return nil, err
	}
	resp, err := b.hc.Do(req)
	if err != nil {
		release()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newError(KindNetwork, "network_unavailable", "request failed: "+scrubURLError(err))
	}
	out := &response{status: resp.StatusCode, header: resp.Header, body: resp.Body, release: release}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, nil
	}
	defer out.close()
	return nil, statusError(resp)
}
func scrubURLError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Op + ": " + ue.Err.Error()
	}
	return err.Error()
}
func statusError(resp *http.Response) *Error {
	var e *Error
	s := resp.StatusCode
	switch {
	case s >= 300 && s < 400:
		e = newError(KindInvalidOutput, "unexpected_redirect", statusMessage(s, "the server answered with a redirect"))
	case s == 401:
		e = newError(KindCredentialExpired, "credential_rejected", statusMessage(s, "the credential was rejected"))
	case s == 403:
		e = newError(KindPermissionDenied, "permission_denied", statusMessage(s, "permission denied"))
	case s == 404:
		e = newError(KindNotFound, "not_found", statusMessage(s, "not found"))
	case s == 429:
		e = newError(KindRateLimited, "rate_limited", statusMessage(s, "rate limited"))
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	case s >= 500:
		e = newError(KindNetwork, "upstream_error", statusMessage(s, "the server reported an error"))
	default:
		msg := statusMessage(s, "the server rejected the request")
		if detail := upstreamMessage(resp.Body); detail != "" {
			msg += ": " + detail
		}
		e = newError(KindInvalidRequest, "bad_request", msg)
	}
	e.Status = s
	return e
}
func upstreamMessage(body io.Reader) string {
	var v struct {
		ErrorMessages []string `json:"errorMessages"`
		Message       any      `json:"message"`
	}
	if json.NewDecoder(io.LimitReader(body, 4096)).Decode(&v) != nil {
		return ""
	}
	s := strings.Join(v.ErrorMessages, "; ")
	if s == "" {
		if m, ok := v.Message.(string); ok {
			s = m
		}
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
func parseRetryAfter(v string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// getJSON decodes a JSON body (8 MiB cap) into v and returns the response headers.
func (b *base) getJSON(ctx context.Context, rawURL string, v any) (http.Header, error) {
	resp, err := b.send(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.close()
	if err = json.NewDecoder(io.LimitReader(resp.body, maxJSONBytes)).Decode(v); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newError(KindInvalidOutput, "invalid_output", "the response was not the expected JSON")
	}
	return resp.header, nil
}

// getRaw reads at most max bytes and reports whether the body was longer.
func (b *base) getRaw(ctx context.Context, rawURL string, max int64) ([]byte, bool, error) {
	resp, err := b.send(ctx, rawURL)
	if err != nil {
		return nil, false, err
	}
	defer resp.close()
	data, err := io.ReadAll(io.LimitReader(resp.body, max+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, newError(KindNetwork, "network_unavailable", "reading the response failed")
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

// flex accepts a JSON string or number and renders it as a string.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(s, "\"") {
		var x string
		if err := json.Unmarshal(b, &x); err != nil {
			return err
		}
		*f = flex(x)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flex(n.String())
	return nil
}
func escape(s string) string { return url.PathEscape(s) }

// fatal reports whether a failed optional call must abort the caller (as opposed to "unknown").
func fatal(err error) bool {
	e, ok := AsError(err)
	return !ok || e.Kind == KindCredentialExpired || e.Kind == KindNetwork || e.Kind == KindRateLimited || e.Kind == KindConfig
}
