package server

import (
	"context"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/catherd"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type DoctorCheck struct {
	ID     string `bson:"id" json:"id"`
	Label  string `bson:"label" json:"label"`
	State  string `bson:"state" json:"state"`
	Word   string `bson:"word" json:"word"`
	Detail string `bson:"detail" json:"detail"`
	Fix    string `bson:"fix" json:"fix"`
}
type DoctorSummary struct {
	OperationID string        `bson:"operation_id" json:"operation_id"`
	CheckedAt   time.Time     `bson:"checked_at" json:"checked_at"`
	Ready       bool          `bson:"ready" json:"ready"`
	Checks      []DoctorCheck `bson:"checks" json:"checks"`
	ArtifactID  string        `bson:"artifact_id" json:"artifact_id"`
	Host        string        `bson:"host" json:"host"`
}
type CatherdInventory struct {
	Names     []string  `bson:"names" json:"names"`
	CheckedAt time.Time `bson:"checked_at" json:"checked_at"`
}
type CatherdCatalogState struct {
	ArtifactID  string    `bson:"artifact_id" json:"artifact_id"`
	RefreshedAt time.Time `bson:"refreshed_at" json:"refreshed_at"`
	ModelCount  int       `bson:"model_count" json:"model_count"`
	Mode        string    `bson:"mode" json:"mode"`
	OperationID string    `bson:"operation_id" json:"operation_id"`
}
type CatherdStateDoc struct {
	ID         string                   `bson:"_id" json:"_id"`
	OrgID      string                   `bson:"org_id" json:"org_id"`
	Version    *string                  `bson:"version" json:"version"`
	LastDoctor *DoctorSummary           `bson:"last_doctor" json:"last_doctor"` // legacy: the codex report
	Doctors    map[string]DoctorSummary `bson:"doctors" json:"doctors"`
	Inventory  *CatherdInventory        `bson:"inventory" json:"inventory"`
	Catalog    *CatherdCatalogState     `bson:"catalog" json:"catalog"`
}

func catherdStateID(org string) string { return "catherd:" + org }

func (a *App) catherdState(ctx context.Context, org string) (*CatherdStateDoc, error) {
	d, err := findOne[CatherdStateDoc](ctx, a.DB, "catherd_states", bson.M{"_id": catherdStateID(org), "org_id": org})
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// SetCatherd installs the catherd port and runtime; tests pass a fake.
func (a *App) SetCatherd(p catherd.Port, rt *catherd.Runtime) { a.Catherd, a.CatherdRT = p, rt }

// InitCatherd resolves the executable and its child PATH and reads its version. It is never fatal.
func (a *App) InitCatherd(ctx context.Context) {
	rt := catherd.NewRuntime(ctx, catherd.RuntimeConfig{Bin: a.Config.CatherdBin, Path: a.Config.CatherdPath, PersonalHome: a.Config.PersonalCatherdHome, DataDir: a.Config.DataDir})
	neutral := filepath.Join(a.Config.DataDir, "catherd", "_neutral")
	_ = ensureDir(neutral)
	a.SetCatherd(&catherd.CLI{RT: rt, Neutral: neutral}, rt)
	a.detectCatherdVersion(ctx)
}
func (a *App) detectCatherdVersion(ctx context.Context) {
	if a.CatherdRT == nil || a.CatherdRT.Bin == "" || a.Catherd == nil {
		return
	}
	a.versionTry.Store(time.Now().UnixNano())
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if v, err := a.Catherd.Version(c); err == nil {
		a.CatherdRT.SetVersion(v)
	}
}
func (a *App) orgLock(org string) *sync.Mutex {
	m, _ := a.orgLocks.LoadOrStore(org, &sync.Mutex{})
	return m.(*sync.Mutex)
}
func (a *App) catherdEnv(org string) (*catherd.OrgEnv, error) {
	env := catherd.NewOrgEnv(a.Config.DataDir, org)
	return env, env.Ensure()
}

// catherdFailure maps a catherd error to a job failure: a missing or unsupported catherd is permanent, a timeout is retried.
func catherdFailure(err error) error {
	var ce *catherd.Error
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case catherd.CodeUnavailable:
		return &JobFailure{Code: "catherd_unavailable", Message: ce.Message}
	case catherd.CodeTimeout:
		return &JobFailure{Code: "catherd_failed", Message: ce.Message, Retryable: true}
	}
	return &JobFailure{Code: "catherd_failed", Message: strings.TrimSpace(ce.Code + ": " + ce.Message)}
}
func (a *App) catherdReady() error {
	if a.Catherd == nil || a.CatherdRT == nil || !a.CatherdRT.Available() {
		return &JobFailure{Code: "catherd_unavailable", Message: "catherd was not found or did not report a version"}
	}
	return nil
}

// catherdProblem is the HTTP form of a catherd failure.
func catherdProblem(err error) error {
	var ce *catherd.Error
	if !errors.As(err, &ce) {
		return err
	}
	if ce.Code == catherd.CodeUnavailable {
		return problem("catherd_unavailable")
	}
	e := problem("catherd_failed")
	e.Detail = strings.TrimSpace(ce.Message)
	return e
}
func (a *App) catherdStatusDTO(ctx context.Context, org string) (bson.M, error) {
	rt := a.CatherdRT
	if rt != nil && rt.Bin != "" && rt.Version() == "" && time.Since(time.Unix(0, a.versionTry.Load())) > 30*time.Second {
		a.detectCatherdVersion(ctx)
	}
	st, err := a.catherdState(ctx, org)
	if err != nil {
		return nil, err
	}
	host := a.orgHost(ctx, org)
	out := bson.M{"host": host, "available": false, "bin": nil, "version": nil, "supported": false, "path_source": nil, "home_display": catherd.NewOrgEnv(a.Config.DataDir, org).Home(), "last_doctor": nil, "inventory": nil, "catalog": nil}
	if rt != nil {
		out["available"], out["supported"] = rt.Available(), rt.Supported()
		out["path_source"] = rt.PathSource
		if rt.Bin != "" {
			out["bin"] = rt.Bin
		}
		if v := rt.Version(); v != "" {
			out["version"] = v
		}
	}
	if st != nil {
		out["last_doctor"] = doctorDTO(st.doctorFor(host)) // the effective host's report: no stale "ready" from another host
		if i := st.Inventory; i != nil {
			names := i.Names
			if names == nil {
				names = []string{}
			}
			out["inventory"] = bson.M{"names": names, "checked_at": timestamp(i.CheckedAt)}
		}
		if c := st.Catalog; c != nil {
			out["catalog"] = bson.M{"refreshed_at": timestamp(c.RefreshedAt), "model_count": c.ModelCount, "mode": c.Mode}
		}
	}
	return out, nil
}
func (a *App) getCatherdStatus(w http.ResponseWriter, r *http.Request) error {
	dto, err := a.catherdStatusDTO(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	writeJSON(w, 200, dto)
	return nil
}
func (a *App) getCatherdSchema(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, 200, bson.M{"roles": catherd.Roles, "access": catherd.Access, "billing_keys": catherd.BillingKeys, "billing_modes": catherd.BillingModes, "harness_keys": catherd.HarnessKeys, "notify": catherd.Notify, "objectives": catherd.Objectives, "jev_uses": catherd.JevUses, "supported_range": ">=1.5.0 <1.6.0", "host": a.orgHost(r.Context(), r.PathValue("orgId"))})
	return nil
}

type RunDoctorRequest struct {
	Host *string `json:"host"`
}

func (a *App) startDoctor(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req RunDoctorRequest
	if r.ContentLength != 0 {
		if err = decodeJSON(w, r, &req, true); err != nil {
			return err
		}
	}
	if req.Host != nil && !catherd.ValidHost(*req.Host) {
		return validation("host", "must be codex or claude-code")
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/catherd/doctor"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		host := ""
		if req.Host != nil {
			host = *req.Host
		} else if host, err = a.orgHostE(ctx, org); err != nil {
			return Output{}, err
		}
		return a.startOperation(ctx, org, "catherd_doctor", Subject{"organization", org}, "catherd_doctor:"+host, 2, bson.M{"host": host})
	})
}

type CatherdCatalogRequest struct {
	Mode string `json:"mode"`
}

func (a *App) startCatherdCatalog(w http.ResponseWriter, r *http.Request) error {
	key, err := idempotencyKey(r, true)
	if err != nil {
		return err
	}
	var req CatherdCatalogRequest
	if err = decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	org := r.PathValue("orgId")
	route := "POST /api/v1/orgs/{orgId}/catherd/catalog/refresh"
	return a.idempotent(w, r, org, route, key, requestHash("POST", route, map[string]string{"orgId": org}, req), func(ctx context.Context) (Output, error) {
		if !contains([]string{"list", "listings", "sync"}, req.Mode) {
			return Output{}, validation("mode", "must be list, listings or sync")
		}
		return a.startOperation(ctx, org, "catherd_catalog", Subject{"organization", org}, "catherd_catalog", 2, bson.M{"mode": req.Mode})
	})
}
func (a *App) getCatherdCatalog(w http.ResponseWriter, r *http.Request) error {
	org := r.PathValue("orgId")
	st, err := a.catherdState(r.Context(), org)
	if err != nil {
		return err
	}
	if st == nil || st.Catalog == nil {
		writeJSON(w, 200, bson.M{"refreshed_at": nil, "models": []bson.M{}})
		return nil
	}
	cat, err := a.loadCatalog(r.Context(), org, st.Catalog.ArtifactID)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	role, backend, text := q.Get("role"), q.Get("backend"), strings.ToLower(strings.TrimSpace(q.Get("text")))
	onlyAvailable, err := boolQuery(r, "available_only")
	if err != nil {
		return err
	}
	if role != "" && !contains(catherd.Roles, role) {
		return validation("role", "unknown role")
	}
	backends := map[string]string{}
	if d := st.doctorFor(a.orgHost(r.Context(), org)); d != nil {
		for _, c := range d.Checks {
			if b, ok := strings.CutPrefix(c.ID, "backend:"); ok {
				switch {
				case c.State == "fail":
					backends[b] = "fail"
				case c.State == "skip" && strings.Contains(strings.ToLower(c.Word), "missing"):
					backends[b] = "skip:missing"
				}
			}
		}
	}
	models := []bson.M{}
	for _, m := range cat.Models {
		if backend != "" && m.Backend != backend {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(m.ID+" "+m.Model+" "+deref(m.Name)), text) {
			continue
		}
		rungs := []bson.M{}
		for _, cr := range m.Rungs {
			av := catherd.Availability(m, cr, role, backends)
			if onlyAvailable && av != catherd.Available && av != catherd.UnknownListing {
				continue
			}
			rung, _ := catherd.ParseRung(cr.Rung)
			reasons := []string{}
			if av != catherd.Available {
				reasons = append(reasons, av)
			}
			item := bson.M{"rung": cr.Rung, "effort": rung.Effort, "scored": cr.Enabled, "treat_like": nil, "cost_tier": nil, "cost_mode": nil, "availability": av, "reasons": reasons, "inferred_only": cr.InferredOnly(), "scores": rawMap(cr.Scores)}
			if cr.TreatLike != nil {
				item["treat_like"] = bson.M{"like": cr.TreatLike.Like, "source": cr.TreatLike.Source}
			}
			if cr.Cost != nil {
				item["cost_tier"], item["cost_mode"] = cr.Cost.Tier, cr.Cost.Mode
			}
			rungs = append(rungs, item)
		}
		if len(rungs) == 0 {
			continue
		}
		var price any
		if len(m.Price) > 0 {
			var p any
			if json.Unmarshal(m.Price, &p) == nil {
				price = p
			}
		}
		efforts, roles := m.Efforts, m.Roles
		if efforts == nil {
			efforts = []string{}
		}
		if roles == nil {
			roles = []string{}
		}
		models = append(models, bson.M{"family_id": m.ID, "name": m.Name, "backend": m.Backend, "model": m.Model, "billing": m.Billing, "efforts": efforts, "roles": roles, "listed": m.Listed, "context": m.Context, "price": price, "rungs": rungs})
	}
	writeJSON(w, 200, bson.M{"refreshed_at": timestamp(st.Catalog.RefreshedAt), "models": models})
	return nil
}
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func rawMap(in map[string]json.RawMessage) bson.M {
	out := bson.M{}
	for k, v := range in {
		var x any
		if json.Unmarshal(v, &x) == nil {
			out[k] = x
		}
	}
	return out
}

// loadCatalog reads a catalog snapshot artifact, keeping the last few parsed in memory.
func (a *App) loadCatalog(ctx context.Context, org, artifactID string) (catherd.Catalog, error) {
	key := org + ":" + artifactID
	a.catalogMu.Lock()
	if c, ok := a.catalogMem[key]; ok {
		a.catalogMu.Unlock()
		return c, nil
	}
	a.catalogMu.Unlock()
	art, err := findOne[ArtifactDoc](ctx, a.DB, "artifacts", bson.M{"_id": artifactID, "org_id": org})
	if err != nil {
		return catherd.Catalog{}, err
	}
	b, err := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), org, art.Sha256)
	if err != nil {
		return catherd.Catalog{}, err
	}
	c, err := catherd.ParseCatalog(b)
	if err != nil {
		return catherd.Catalog{}, err
	}
	a.catalogMu.Lock()
	if a.catalogMem == nil || len(a.catalogMem) >= 8 {
		a.catalogMem = map[string]catherd.Catalog{}
	}
	a.catalogMem[key] = c
	a.catalogMu.Unlock()
	return c, nil
}
func (a *App) getPersonalProfiles(w http.ResponseWriter, r *http.Request) error {
	if a.Catherd == nil || a.CatherdRT == nil || !a.CatherdRT.Available() {
		writeJSON(w, 200, bson.M{"available": false, "profiles": []bson.M{}})
		return nil
	}
	env := catherd.NewPersonalEnv(a.Config.DataDir)
	if err := env.Ensure(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := a.Catherd.ListProfiles(ctx, env)
	if err != nil {
		return catherdProblem(err)
	}
	profiles := []bson.M{}
	for _, p := range rows {
		profiles = append(profiles, bson.M{"name": p.Name, "active": p.Active})
	}
	writeJSON(w, 200, bson.M{"available": true, "profiles": profiles})
	return nil
}

var upsertOpts = options.UpdateOne().SetUpsert(true)
