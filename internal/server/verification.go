package server

import (
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"orqen/internal/gitdiff"
	"orqen/internal/verify"
	"path/filepath"
	"time"
)

// EvidenceCommand is one command's result in the evidence.
type EvidenceCommand struct {
	Key              string   `bson:"key"`
	Fingerprint      string   `bson:"fingerprint"`
	Argv             []string `bson:"argv"`
	Cwd              string   `bson:"cwd"`
	ExitCode         int      `bson:"exit_code"`
	DurationMs       int64    `bson:"duration_ms"`
	TimedOut         bool     `bson:"timed_out"`
	Sandbox          string   `bson:"sandbox"`
	Outcome          string   `bson:"outcome"` // pass | fail | new_failure | baseline_failure | incomplete
	Reason           string   `bson:"reason"`
	StdoutArtifactID string   `bson:"stdout_artifact_id"`
	StderrArtifactID string   `bson:"stderr_artifact_id"`
	OutputSHA256     string   `bson:"output_sha256"`
}

// EvidenceSignal is one weakening signal.
type EvidenceSignal struct {
	Code   string `bson:"code"`
	Path   string `bson:"path"`
	Detail string `bson:"detail"`
}

// EvidenceVerifier is the verifier role's verdict.
type EvidenceVerifier struct {
	RexID      string `bson:"rex_id"`
	Verdict    string `bson:"verdict"`
	ArtifactID string `bson:"artifact_id"`
}

// EvidenceGate is the verdict of both layers.
type EvidenceGate struct {
	Status  string   `bson:"status"`
	Reasons []string `bson:"reasons"`
}

// EvidenceWaiver is a user's audited acceptance of a weakening suspicion.
type EvidenceWaiver struct {
	Text string    `bson:"text"`
	At   time.Time `bson:"at"`
}

// VerificationEvidenceDoc is what Orqen itself established about a commit (collection verification_evidence).
type VerificationEvidenceDoc struct {
	ID             string            `bson:"_id"`
	OrgID          string            `bson:"org_id"`
	RunID          string            `bson:"run_id"`
	ProjectID      string            `bson:"project_id"`
	Round          int               `bson:"round"`
	Kind           string            `bson:"kind"` // baseline | head
	BaseSHA        string            `bson:"base_sha"`
	HeadSHA        string            `bson:"head_sha"`
	CommandsFP     string            `bson:"commands_fp"`
	Commands       []EvidenceCommand `bson:"commands"`
	DiffArtifactID string            `bson:"diff_artifact_id"`
	DiffSHA        string            `bson:"diff_sha"`
	Suspected      bool              `bson:"weakening_suspected"`
	Signals        []EvidenceSignal  `bson:"weakening_signals"`
	Verifier       *EvidenceVerifier `bson:"verifier"`
	Gate           EvidenceGate      `bson:"gate"`
	Waiver         *EvidenceWaiver   `bson:"waiver"`
	CreatedAt      time.Time         `bson:"created_at"`
}

// DTO is VerificationEvidenceDto.
func (d VerificationEvidenceDoc) DTO() bson.M {
	cmds := []bson.M{}
	for _, c := range d.Commands {
		cmds = append(cmds, bson.M{"key": c.Key, "fingerprint": c.Fingerprint, "argv": nonNilStr(c.Argv), "cwd": c.Cwd, "exit_code": c.ExitCode, "duration_ms": c.DurationMs, "timed_out": c.TimedOut, "sandbox": c.Sandbox, "outcome": c.Outcome, "reason": c.Reason,
			"stdout_artifact_id": optStr(c.StdoutArtifactID), "stderr_artifact_id": optStr(c.StderrArtifactID), "output_sha256": c.OutputSHA256})
	}
	signals := []bson.M{}
	for _, s := range d.Signals {
		signals = append(signals, bson.M{"code": s.Code, "path": s.Path, "detail": s.Detail})
	}
	var ver, waiver any
	if d.Verifier != nil {
		ver = bson.M{"rex_id": d.Verifier.RexID, "verdict": d.Verifier.Verdict, "artifact_id": optStr(d.Verifier.ArtifactID)}
	}
	if d.Waiver != nil {
		waiver = bson.M{"text": d.Waiver.Text, "at": timestamp(d.Waiver.At)}
	}
	return bson.M{"id": d.ID, "run_id": d.RunID, "round": d.Round, "kind": d.Kind, "base_sha": d.BaseSHA, "head_sha": d.HeadSHA, "commands_fp": d.CommandsFP, "commands": cmds,
		"diff_artifact_id": optStr(d.DiffArtifactID), "diff_sha256": d.DiffSHA, "weakening": bson.M{"suspected": d.Suspected, "signals": signals}, "verifier": ver,
		"gate": bson.M{"status": d.Gate.Status, "reasons": nonNilStr(d.Gate.Reasons)}, "waiver": waiver, "created_at": timestamp(d.CreatedAt)}
}

func (a *App) listRunVerification(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	if _, err := a.run(r.Context(), org, id); err != nil {
		return err
	}
	docs, err := findMany[VerificationEvidenceDoc](r.Context(), a.DB, "verification_evidence", bson.M{"org_id": org, "run_id": id}, bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}, 200)
	if err != nil {
		return err
	}
	out := []bson.M{}
	for _, d := range docs {
		out = append(out, d.DTO())
	}
	writeJSON(w, 200, bson.M{"items": out, "next_cursor": nil})
	return nil
}

// getRunDiff is GET /runs/{runId}/diff?head=: the file list of base..head and the diff artifact.
func (a *App) getRunDiff(w http.ResponseWriter, r *http.Request) error {
	org, id := r.PathValue("orgId"), r.PathValue("runId")
	run, err := a.run(r.Context(), org, id)
	if err != nil {
		return err
	}
	head := r.URL.Query().Get("head")
	if head == "" {
		head = run.Heads.Head
	}
	if head == "" {
		return problem("not_found")
	}
	ev, err := findOne[VerificationEvidenceDoc](r.Context(), a.DB, "verification_evidence", bson.M{"org_id": org, "run_id": id, "kind": "head", "head_sha": head, "diff_artifact_id": bson.M{"$ne": ""}})
	if errors.Is(err, mongo.ErrNoDocuments) {
		return problem("not_found")
	}
	if err != nil {
		return err
	}
	art, err := findOne[ArtifactDoc](r.Context(), a.DB, "artifacts", bson.M{"_id": ev.DiffArtifactID, "org_id": org})
	if err != nil {
		return err
	}
	data, err := readBlob(filepath.Join(a.Config.DataDir, "artifacts"), org, art.Sha256)
	if err != nil {
		return err
	}
	diff, err := gitdiff.Parse(string(data))
	if err != nil {
		return err
	}
	files := []bson.M{}
	for _, f := range diff.Files {
		files = append(files, bson.M{"path": f.Path, "status": f.Status, "additions": f.Additions, "deletions": f.Deletions})
	}
	signals := []bson.M{}
	for _, s := range ev.Signals {
		signals = append(signals, bson.M{"code": s.Code, "path": s.Path, "detail": s.Detail})
	}
	writeJSON(w, 200, bson.M{"base_sha": ev.BaseSHA, "head_sha": ev.HeadSHA, "files": files, "artifact_id": ev.DiffArtifactID, "weakening": bson.M{"suspected": ev.Suspected, "signals": signals}})
	return nil
}

var _ = verify.GatePass
