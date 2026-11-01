package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"

	"orqen/internal/intake"
)

// DefaultMaxPageBytes is the cap of a page body (policy docs.max_page_bytes).
const DefaultMaxPageBytes = 1 << 20

type confluence struct {
	base
	cloud        bool
	maxPageBytes int
	prefix       string // path prefix of the REST API ("" for Server/DC, "/wiki" for Cloud unless the base has it)
}

// NewConfluence returns a read-only DocsSource. Server/Data Center keeps the base path (a context path) exactly;
// Cloud appends /wiki unless the base already ends in /wiki. The adapter has no write method at all.
func NewConfluence(o Options, auth Auth, deployment string, maxPageBytes int) DocsSource {
	auth.Provider, auth.Deployment = "confluence", deployment
	c := &confluence{base: newBase(o, auth), cloud: deployment == "cloud", maxPageBytes: maxPageBytes}
	if c.maxPageBytes <= 0 {
		c.maxPageBytes = DefaultMaxPageBytes
	}
	if c.cloud && c.root != nil && !strings.HasSuffix(strings.TrimRight(c.root.Path, "/"), "/wiki") {
		c.prefix = "/wiki"
	}
	return c
}

func (c *confluence) u(path string, q url.Values) string {
	if c.root == nil {
		return ""
	}
	s := strings.TrimRight(c.root.String(), "/") + c.prefix + path
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s
}

func (c *confluence) Identity(ctx context.Context) (Identity, error) {
	var m struct {
		AccountID   string `json:"accountId"`
		UserKey     string `json:"userKey"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		PublicName  string `json:"publicName"`
	}
	if _, err := c.getJSON(ctx, c.u("/rest/api/user/current", nil), &m); err != nil {
		if e, ok := AsError(err); ok && e.Kind == KindNotFound {
			return Identity{}, &Error{Kind: KindUnsupported, Code: "api_not_found", Message: "the Confluence API was not found at this URL", Status: e.Status}
		}
		return Identity{}, err
	}
	return Identity{ProviderUserID: first(m.AccountID, m.UserKey, m.Username), Username: first(m.Username, m.Email, m.DisplayName),
		DisplayName: first(m.DisplayName, m.PublicName)}, nil
}

func (c *confluence) Capabilities(ctx context.Context) (Capabilities, error) {
	caps := Capabilities{Features: map[string]string{}, Deployment: map[bool]string{true: "cloud", false: "server"}[c.cloud]}
	probe := c.u("/rest/api/content", url.Values{"limit": {"1"}})
	if c.cloud {
		probe = c.u("/api/v2/pages", url.Values{"limit": {"1"}})
	}
	_, err := c.getJSON(ctx, probe, &struct{}{})
	switch {
	case err == nil:
		caps.Features["read_pages"] = FeatureAvailable
	case fatal(err):
		return caps, err
	default:
		e, _ := AsError(err)
		if e.Kind == KindPermissionDenied {
			caps.Features["read_pages"] = FeatureDenied
		} else {
			caps.Features["read_pages"] = FeatureUnknown
		}
	}
	return caps, nil
}

type confBody struct {
	Storage struct {
		Value string `json:"value"`
	} `json:"storage"`
}
type confPage struct {
	ID      flex   `json:"id"`
	Title   string `json:"title"`
	SpaceID flex   `json:"spaceId"`
	Space   struct {
		Key string `json:"key"`
	} `json:"space"`
	Version struct {
		Number    int    `json:"number"`
		When      string `json:"when"`
		CreatedAt string `json:"createdAt"`
	} `json:"version"`
	Body  confBody `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
		Base  string `json:"base"`
	} `json:"_links"`
}

func (c *confluence) page(p confPage, id string) Page {
	raw := p.Body.Storage.Value
	truncated := false
	if len(raw) > c.maxPageBytes {
		raw, truncated = raw[:c.maxPageBytes], true
		for len(raw) > 0 && raw[len(raw)-1] >= 0x80 && raw[len(raw)-1] < 0xC0 {
			raw = raw[:len(raw)-1]
		}
	}
	text, links := ConfluenceStorageToText(raw)
	if len(text) > c.maxPageBytes {
		text, truncated = text[:c.maxPageBytes], true
	}
	h := sha256.Sum256([]byte(intake.Normalize(text)))
	r := sha256.Sum256([]byte(p.Body.Storage.Value))
	pid := string(p.ID)
	if pid == "" {
		pid = id
	}
	web := ""
	if p.Links.WebUI != "" {
		b := strings.TrimRight(p.Links.Base, "/")
		if b == "" {
			b = strings.TrimRight(c.root.String(), "/") + c.prefix
		}
		web = b + p.Links.WebUI
	}
	return Page{ID: pid, Title: p.Title, SpaceKey: first(p.Space.Key, string(p.SpaceID)), URL: web, Version: p.Version.Number,
		UpdatedAt: first(p.Version.When, p.Version.CreatedAt), Storage: raw, Text: text, Links: links,
		ContentHash: hex.EncodeToString(h[:]), RawHash: hex.EncodeToString(r[:]), Truncated: truncated}
}

func (c *confluence) GetPage(ctx context.Context, id string) (Page, error) {
	return c.fetch(ctx, id, 0)
}
func (c *confluence) GetPageVersion(ctx context.Context, id string, n int) (Page, error) {
	return c.fetch(ctx, id, n)
}

func (c *confluence) fetch(ctx context.Context, id string, version int) (Page, error) {
	var p confPage
	var u string
	switch {
	case c.cloud && version == 0:
		u = c.u("/api/v2/pages/"+escape(id), url.Values{"body-format": {"storage"}})
	case c.cloud:
		u = c.u("/api/v2/pages/"+escape(id), url.Values{"version": {strconv.Itoa(version)}, "body-format": {"storage"}})
	case version == 0:
		u = c.u("/rest/api/content/"+escape(id), url.Values{"expand": {"body.storage,version,space,history.lastUpdated"}})
	default:
		u = c.u("/rest/api/content/"+escape(id), url.Values{"status": {"historical"}, "version": {strconv.Itoa(version)}, "expand": {"body.storage,version"}})
	}
	if _, err := c.getJSON(ctx, u, &p); err != nil {
		return Page{}, err
	}
	return c.page(p, id), nil
}

func (c *confluence) Search(ctx context.Context, cql string, limit int) ([]PageRef, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := url.Values{"cql": {cql}, "limit": {strconv.Itoa(limit)}}
	var out []PageRef
	if c.cloud {
		var r struct {
			Results []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content struct {
					ID    flex   `json:"id"`
					Title string `json:"title"`
					Space struct {
						Key string `json:"key"`
					} `json:"space"`
				} `json:"content"`
			} `json:"results"`
		}
		if _, err := c.getJSON(ctx, c.u("/rest/api/search", q), &r); err != nil {
			return nil, err
		}
		for _, x := range r.Results {
			out = append(out, PageRef{ID: string(x.Content.ID), Title: first(x.Content.Title, x.Title), SpaceKey: x.Content.Space.Key, URL: x.URL})
		}
		return out, nil
	}
	var r struct {
		Results []confPage `json:"results"`
	}
	if _, err := c.getJSON(ctx, c.u("/rest/api/content/search", q), &r); err != nil {
		return nil, err
	}
	for _, x := range r.Results {
		out = append(out, PageRef{ID: string(x.ID), Title: x.Title, SpaceKey: x.Space.Key, URL: x.Links.WebUI})
	}
	return out, nil
}
