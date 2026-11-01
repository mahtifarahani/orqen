package sysprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

type ConnTarget struct {
	ID, Name    string
	BaseURL     string
	ProxyURL    string
	CustomCAPEM string
	Timeout     time.Duration
}

// NetResult is what a connection probe saw. Stage is the first failing stage (dns, tcp, tls, proxy), empty on success.
type NetResult struct {
	Stage   string
	Err     string
	Addrs   []string
	Private bool // a resolved address is RFC1918 or CGNAT
}

type Network interface {
	Probe(ctx context.Context, t ConnTarget) NetResult
}

func redactProxy(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "set"
	}
	return u.Scheme + "://" + u.Host
}

func privateAddr(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true // 100.64.0.0/10
	}
	return ip.IsPrivate()
}

// RealNetwork resolves, dials and (for https) handshakes with TLS, through the connection's proxy and CA.
type RealNetwork struct{}

func (RealNetwork) Probe(ctx context.Context, t ConnTarget) NetResult {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil || u.Hostname() == "" {
		return NetResult{Stage: "dns", Err: "the base URL has no host"}
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	dialHost, dialPort, stageTCP := u.Hostname(), port, "tcp"
	if t.ProxyURL != "" {
		p, err := url.Parse(t.ProxyURL)
		if err != nil || p.Hostname() == "" {
			return NetResult{Stage: "proxy", Err: "the proxy URL is invalid"}
		}
		dialHost, dialPort, stageTCP = p.Hostname(), p.Port(), "proxy"
		if dialPort == "" {
			dialPort = "1080"
		}
	}
	lookup, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookup, dialHost)
	res := NetResult{}
	if err != nil {
		res.Stage, res.Err = "dns", "cannot resolve "+dialHost+": "+err.Error()
		return res
	}
	for _, a := range addrs {
		res.Addrs = append(res.Addrs, a.IP.String())
		if privateAddr(a.IP) {
			res.Private = true
		}
	}
	var conn net.Conn
	d := net.Dialer{Timeout: timeout}
	for _, a := range addrs {
		if conn, err = d.DialContext(ctx, "tcp", net.JoinHostPort(a.IP.String(), dialPort)); err == nil {
			break
		}
	}
	if err != nil {
		res.Stage, res.Err = stageTCP, err.Error()
		return res
	}
	defer conn.Close()
	if t.ProxyURL != "" || u.Scheme != "https" {
		return res
	}
	cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12}
	if t.CustomCAPEM != "" {
		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM([]byte(t.CustomCAPEM))
		cfg.RootCAs = pool
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if err = tls.Client(conn, cfg).HandshakeContext(ctx); err != nil {
		res.Stage, res.Err = "tls", err.Error()
	}
	return res
}

// caExpiry returns the earliest NotAfter among the PEM certificates.
func caExpiry(pemText string) (time.Time, bool) {
	var earliest time.Time
	found := false
	rest := []byte(pemText)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil && (!found || cert.NotAfter.Before(earliest)) {
			earliest, found = cert.NotAfter, true
		}
	}
	return earliest, found
}
func networkChecks(ctx context.Context, d Deps, in Inputs) []Check {
	var out []Check
	for _, t := range in.Connections {
		out = append(out, timed(func() Check {
			c := Check{ID: "net." + t.ID, Group: "network", Blocks: []string{"connection:" + t.ID}}
			var notes []string
			if t.CustomCAPEM != "" {
				if exp, ok := caExpiry(t.CustomCAPEM); ok {
					switch left := exp.Sub(d.Now()); {
					case left <= 0:
						c.Status, c.FixCode, c.Detail = Fail, "ca_expired", t.Name+": the custom CA certificate expired on "+exp.Format("2006-01-02")
						return c
					case left < 30*24*time.Hour:
						notes = append(notes, fmt.Sprintf("the custom CA expires in %d days", int(left.Hours()/24)))
					}
				}
			}
			r := d.Net.Probe(ctx, t)
			switch {
			case r.Stage == "":
				c.Status, c.Detail = Pass, t.Name+": DNS, TCP and TLS succeeded"
				if t.ProxyURL != "" {
					c.Detail = t.Name + ": the proxy is reachable"
				}
				if len(notes) > 0 {
					c.Status, c.FixCode = Warn, "ca_expiring"
					c.Detail += "; " + strings.Join(notes, "; ")
				}
			default:
				c.Status, c.FixCode, c.Detail = Fail, r.Stage+"_failed", t.Name+": "+r.Stage+" failed: "+r.Err
				if r.Private && (r.Stage == "tcp" || r.Stage == "tls") {
					c.FixCode = "vpn_required_maybe"
					c.Detail += " (the address is private; a VPN may be required)"
				}
				if r.Stage == "dns" {
					c.FixCode = "dns_failed"
				}
			}
			return c
		}))
	}
	return out
}
