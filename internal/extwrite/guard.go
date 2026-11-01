// Package extwrite is the only place where Orqen may write to something outside its own process: pushes, merge
// request changes and comments. Every write is decided by the pure Decide function, re-decided inside Execute and
// sent through a transport that refuses non-loopback addresses in every development and test build.
package extwrite

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// BuildChannel is "dev" unless `make release` sets it to "release" with -ldflags -X.
var BuildChannel = "dev"

// Scope says which hosts an external write may reach.
type Scope string

const (
	ScopeLoopbackOnly Scope = "loopback_only"
	ScopeAnyHost      Scope = "any_host"
)

// Guard is the environment guard as shown in the UI.
type Guard struct {
	BuildChannel string
	OrqenEnv     string
	Scope        Scope
	KillSwitch   bool
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// orqenEnv normalizes ORQEN_ENV to dev, test or prod (anything else is dev).
func orqenEnv(getenv func(string) string) string {
	switch v := strings.ToLower(strings.TrimSpace(getenv("ORQEN_ENV"))); v {
	case "test", "prod":
		return v
	}
	return "dev"
}

// CurrentScope is any_host only for a release build running with ORQEN_ENV=prod and without ORQEN_DEV. A go test
// binary is always loopback only.
func CurrentScope(getenv func(string) string) Scope {
	if testing.Testing() {
		return ScopeLoopbackOnly
	}
	if BuildChannel == "release" && orqenEnv(getenv) == "prod" && !truthy(getenv("ORQEN_DEV")) {
		return ScopeAnyHost
	}
	return ScopeLoopbackOnly
}

// KillSwitch reports ORQEN_EXTERNAL_WRITES=off.
func KillSwitch(getenv func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(getenv("ORQEN_EXTERNAL_WRITES")), "off")
}

// CurrentGuard collects the guard values for the settings API.
func CurrentGuard(getenv func(string) string) Guard {
	return Guard{BuildChannel: BuildChannel, OrqenEnv: orqenEnv(getenv), Scope: CurrentScope(getenv), KillSwitch: KillSwitch(getenv)}
}

// IsLoopbackHost accepts 127.0.0.1, ::1 and localhost, with or without port or brackets. Other 127.x addresses are
// loopback too, but only the names above are accepted in a URL.
func IsLoopbackHost(host string) bool {
	h := hostOnly(host)
	return h == "127.0.0.1" || h == "::1" || strings.EqualFold(h, "localhost")
}

// IsLoopbackIPLiteral accepts only 127.0.0.1 and ::1 (used for git pushes, which must not resolve names).
func IsLoopbackIPLiteral(host string) bool {
	h := hostOnly(host)
	return h == "127.0.0.1" || h == "::1"
}

func hostOnly(host string) string {
	h := strings.TrimSpace(host)
	if sh, _, err := net.SplitHostPort(h); err == nil {
		h = sh
	}
	return strings.Trim(h, "[]")
}

// ErrNotLoopback is returned when a write would leave the loopback interface in a loopback-only scope.
var ErrNotLoopback = errors.New("extwrite: the target is not a loopback address")

// CheckURL applies the loopback rules for a target URL. Git pushes accept IP literals only.
func CheckURL(scope Scope, raw string, git bool) error {
	if scope != ScopeLoopbackOnly {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ErrNotLoopback
	}
	if git {
		if !IsLoopbackIPLiteral(u.Host) {
			return ErrNotLoopback
		}
		return nil
	}
	if !IsLoopbackHost(u.Host) {
		return ErrNotLoopback
	}
	return nil
}

// Dialer connects write requests. In a loopback-only scope it resolves names itself, requires every address to be
// loopback and dials the literal IP, so a hostname cannot rebind to a public address after the check.
type Dialer struct {
	Scope  Scope
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
	Dial   func(ctx context.Context, network, addr string) (net.Conn, error)
	dials  atomic.Int64
}

// Dials is the number of connections actually opened.
func (d *Dialer) Dials() int64 { return d.dials.Load() }

func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	target := addr
	if d.Scope == ScopeLoopbackOnly {
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			look := d.Lookup
			if look == nil {
				look = func(ctx context.Context, h string) ([]net.IP, error) {
					as, e := net.DefaultResolver.LookupIPAddr(ctx, h)
					out := make([]net.IP, 0, len(as))
					for _, a := range as {
						out = append(out, a.IP)
					}
					return out, e
				}
			}
			if ips, err = look(ctx, host); err != nil {
				return nil, err
			}
		}
		if len(ips) == 0 {
			return nil, ErrNotLoopback
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, ErrNotLoopback
			}
		}
		target = net.JoinHostPort(ips[0].String(), port)
	}
	dial := d.Dial
	if dial == nil {
		nd := &net.Dialer{Timeout: 10 * time.Second}
		dial = nd.DialContext
	}
	d.dials.Add(1)
	return dial(ctx, network, target)
}

// NewClient is the HTTP client for external writes: no proxy in a loopback-only scope, no redirects, a 30 s timeout.
func NewClient(scope Scope, d *Dialer, tlsConf *tls.Config) *http.Client {
	if d == nil {
		d = &Dialer{Scope: scope}
	}
	d.Scope = scope
	tr := &http.Transport{DialContext: d.DialContext, TLSClientConfig: tlsConf, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, DisableKeepAlives: true}
	if scope != ScopeLoopbackOnly {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
