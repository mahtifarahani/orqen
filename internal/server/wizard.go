package server

import (
	"context"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"orqen/internal/wizard"
	"time"
	"unicode/utf8"
)

type WizardStep struct {
	Key        string     `bson:"key" json:"key"`
	Status     string     `bson:"status" json:"status"`
	SkipReason *string    `bson:"skip_reason" json:"skip_reason"`
	UpdatedAt  *time.Time `bson:"updated_at" json:"updated_at"`
}
type WizardDoc struct {
	ID          string       `bson:"_id" json:"_id"`
	OrgID       string       `bson:"org_id" json:"org_id"`
	Version     int          `bson:"version" json:"version"`
	Steps       []WizardStep `bson:"steps" json:"steps"`
	CurrentStep string       `bson:"current_step" json:"current_step"`
	Dismissed   bool         `bson:"dismissed" json:"dismissed"`
	CompletedAt *time.Time   `bson:"completed_at" json:"completed_at"`
	Revision    int64        `bson:"revision" json:"revision"`
	CreatedAt   time.Time    `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time    `bson:"updated_at" json:"updated_at"`
}

func wizardID(org string) string { return "wiz_" + org[4:] }

// defaultWizard is what GET shows before anything was saved: every step todo, revision 0.
func defaultWizard(org string) WizardDoc {
	d := WizardDoc{ID: wizardID(org), OrgID: org, Version: 1, CurrentStep: wizard.Steps[0]}
	for _, k := range wizard.Steps {
		d.Steps = append(d.Steps, WizardStep{Key: k, Status: wizard.Todo})
	}
	return d
}
func (a *App) wizardDoc(ctx context.Context, org string) (WizardDoc, error) {
	d, err := findOne[WizardDoc](ctx, a.DB, "setup_wizards", bson.M{"_id": wizardID(org), "org_id": org})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return defaultWizard(org), nil
	}
	return d, err
}

// wizardInputs reads the existing loaders; nothing here consults wizard state.
func (a *App) wizardInputs(ctx context.Context, org string) (wizard.Inputs, error) {
	in := wizard.Inputs{Now: time.Now().UTC()}
	conns, err := findMany[ConnectionDoc](ctx, a.DB, "connections", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil {
		return in, err
	}
	for _, c := range conns {
		ready := len(c.readinessReasons()) == 0
		in.GitLabReady = in.GitLabReady || ready && c.Provider == "gitlab"
		in.JiraReady = in.JiraReady || ready && c.Provider == "jira"
	}
	projects, err := findMany[ProjectDoc](ctx, a.DB, "projects", bson.M{"org_id": org, "active": true}, bson.D{{Key: "_id", Value: 1}}, 1000)
	if err != nil {
		return in, err
	}
	results, err := a.readinessResults(ctx, org, projects)
	if err != nil {
		return in, err
	}
	for _, r := range results {
		p := wizard.ProjectInput{}
		for _, b := range r.Blockers {
			p.Blockers = append(p.Blockers, b.Code)
		}
		in.Projects = append(in.Projects, p)
	}
	n, err := a.DB.Coll("policy_versions").CountDocuments(ctx, bson.M{"org_id": org, "kind": "org", "scope_id": org})
	if err != nil {
		return in, err
	}
	in.OrgPolicyPublished = n > 0
	st, err := a.systemState(ctx, org)
	if err != nil {
		return in, err
	}
	if p := st.LastPreflight; p != nil {
		at := p.CheckedAt
		in.PreflightAt = &at
		for _, c := range p.Checks {
			in.PreflightRequiredFailed = in.PreflightRequiredFailed || c.Required && c.Status == "fail"
		}
	}
	return in, nil
}
func wizardDTO(d WizardDoc, in wizard.Inputs) bson.M {
	steps := []bson.M{}
	pending := 0
	for _, s := range d.Steps {
		missing := []bson.M{}
		ms := wizard.Evaluate(s.Key, in)
		for _, m := range ms {
			missing = append(missing, bson.M{"code": m.Code, "detail": m.Detail})
		}
		attention := s.Status == wizard.Done && len(ms) > 0
		if s.Status != wizard.Done || attention {
			pending++
		}
		steps = append(steps, bson.M{"key": s.Key, "status": s.Status, "missing": missing, "consequences": wizard.Consequences(s.Key), "attention": attention, "skip_reason": s.SkipReason, "updated_at": optionalTime(s.UpdatedAt)})
	}
	return bson.M{"steps": steps, "current_step": d.CurrentStep, "dismissed": d.Dismissed, "completed_at": optionalTime(d.CompletedAt), "pending_count": pending, "revision": d.Revision}
}

// WizardDoc.DTO is the contract shape without evaluation (used by the contract test).
func (d WizardDoc) DTO() bson.M { return wizardDTO(d, wizard.Inputs{}) }
func (a *App) wizardResponse(ctx context.Context, w http.ResponseWriter, status int, d WizardDoc) error {
	in, err := a.wizardInputs(ctx, d.OrgID)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(d.Revision))
	writeJSON(w, status, wizardDTO(d, in))
	return nil
}
func (a *App) getSetup(w http.ResponseWriter, r *http.Request) error {
	d, err := a.wizardDoc(r.Context(), r.PathValue("orgId"))
	if err != nil {
		return err
	}
	return a.wizardResponse(r.Context(), w, 200, d)
}

type PatchSetupRequest struct {
	CurrentStep *string `json:"current_step"`
	Dismissed   *bool   `json:"dismissed"`
}
type SkipStepRequest struct {
	Reason *string `json:"reason"`
}

var errWizardConflict = errors.New("wizard conflict")

// mutateWizard applies change to the current document under If-Match (revision 0 means "not saved yet").
func (a *App) mutateWizard(w http.ResponseWriter, r *http.Request, event string, change func(cur WizardDoc, now time.Time) (WizardDoc, bson.M, error)) error {
	expected, err := ifMatchOrZero(r)
	if err != nil {
		return err
	}
	org := r.PathValue("orgId")
	var out WizardDoc
	err = a.DB.Txn(r.Context(), func(ctx context.Context) error {
		cur, e := a.wizardDoc(ctx, org)
		if e != nil {
			return e
		}
		if cur.Revision != expected {
			return errWizardConflict
		}
		now := time.Now().UTC()
		next, payload, e := change(cur, now)
		if e != nil {
			return e
		}
		next.UpdatedAt, next.Revision = now, expected+1
		if expected == 0 {
			next.CreatedAt = now
			if _, e = a.DB.Coll("setup_wizards").InsertOne(ctx, next); e != nil {
				if mongo.IsDuplicateKeyError(e) {
					return errWizardConflict
				}
				return e
			}
		} else {
			res, e := a.DB.Coll("setup_wizards").ReplaceOne(ctx, bson.M{"_id": next.ID, "org_id": org, "revision": expected}, next, options.Replace())
			if e != nil {
				return e
			}
			if res.MatchedCount == 0 {
				return errWizardConflict
			}
		}
		out = next
		if event == "" {
			return nil
		}
		return a.AppendEvent(ctx, org, event, Subject{"setup", next.ID}, payload, &next.Revision, nil, false)
	})
	if errors.Is(err, errWizardConflict) {
		cur, e := a.wizardDoc(r.Context(), org)
		if e != nil {
			return e
		}
		conflict := problem("revision_conflict")
		conflict.Current = cur.DTO()
		return conflict
	}
	if err != nil {
		return err
	}
	a.Committed()
	return a.wizardResponse(r.Context(), w, 200, out)
}
func (a *App) patchSetup(w http.ResponseWriter, r *http.Request) error {
	var req PatchSetupRequest
	if err := decodeJSON(w, r, &req, true); err != nil {
		return err
	}
	if req.CurrentStep == nil && req.Dismissed == nil {
		return validation("body", "no changes given")
	}
	if req.CurrentStep != nil && !wizard.ValidStep(*req.CurrentStep) {
		return validation("current_step", "unknown step")
	}
	return a.mutateWizard(w, r, "", func(cur WizardDoc, _ time.Time) (WizardDoc, bson.M, error) {
		if req.CurrentStep != nil {
			cur.CurrentStep = *req.CurrentStep
		}
		if req.Dismissed != nil {
			cur.Dismissed = *req.Dismissed
		}
		return cur, nil, nil
	})
}

// stepAction handles complete, skip and reopen. Skip changes only the step's own status: it never writes a domain
// document, never enables automation and never publishes or binds anything. The readiness gate does not read it.
func (a *App) stepAction(action string) endpoint {
	return func(w http.ResponseWriter, r *http.Request) error {
		key := r.PathValue("stepKey")
		if !wizard.ValidStep(key) {
			return problem("not_found")
		}
		var reason *string
		if action == "skip" {
			var req SkipStepRequest
			if r.ContentLength != 0 {
				if err := decodeJSON(w, r, &req, true); err != nil {
					return err
				}
			}
			if req.Reason != nil {
				if utf8.RuneCountInString(*req.Reason) > 500 {
					return validation("reason", "must be at most 500 characters")
				}
				reason = trimmedPtr(req.Reason)
			}
		}
		org := r.PathValue("orgId")
		if action == "complete" {
			in, err := a.wizardInputs(r.Context(), org)
			if err != nil {
				return err
			}
			if missing := wizard.Evaluate(key, in); len(missing) > 0 {
				e := problem("wizard_step_incomplete")
				for _, m := range missing {
					e.Errors = append(e.Errors, FieldError{m.Code, m.Detail})
				}
				return e
			}
		}
		event := map[string]string{"complete": "setup.step_completed", "skip": "setup.step_skipped", "reopen": "setup.step_reopened"}[action]
		status := map[string]string{"complete": wizard.Done, "skip": wizard.Skipped, "reopen": wizard.Todo}[action]
		return a.mutateWizard(w, r, event, func(cur WizardDoc, now time.Time) (WizardDoc, bson.M, error) {
			for i := range cur.Steps {
				if cur.Steps[i].Key == key {
					cur.Steps[i].Status, cur.Steps[i].SkipReason, cur.Steps[i].UpdatedAt = status, reason, &now
				}
			}
			return cur, bson.M{"step": key, "status": status}, nil
		})
	}
}
func (a *App) finishSetup(w http.ResponseWriter, r *http.Request) error {
	return a.mutateWizard(w, r, "setup.finished", func(cur WizardDoc, now time.Time) (WizardDoc, bson.M, error) {
		cur.CompletedAt = &now
		pending := 0
		for _, s := range cur.Steps {
			if s.Status != wizard.Done {
				pending++
			}
		}
		return cur, bson.M{"pending_steps": pending}, nil
	})
}
