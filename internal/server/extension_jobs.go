package server

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orqen/internal/extensions"
	"orqen/internal/semver"
)

// extExec runs one allowlisted verb of an extension with an explicit environment and a best-effort sandbox
// (no network, writes only under the extension's own work directory).
func (a *App) extExec(ctx context.Context, p extensions.Permit, org string, args []string, env []string) extensions.Result {
	dir := filepath.Join(a.Config.DataDir, "extensions", org, p.Extension())
	_ = os.MkdirAll(dir, 0o700)
	spec := extensions.Spec{Bin: p.Path(), Args: args, Dir: dir, Timeout: 20 * time.Second, StdoutCap: 64 << 10,
		Env:     extensions.BuildEnv([]string{"PATH=" + "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin", "LANG=C", "HOME=" + dir}, nil, nil),
		Sandbox: extensions.DenyNetworkWriteUnder(dir), SandboxMode: extensions.SandboxBestEffort}
	if a.noSandbox {
		spec.Sandbox = ""
	}
	return extensions.Exec(ctx, p, spec)
}

// extFinish writes the outcome of a detect, test or health job and completes it.
func (a *App) extFinish(ctx context.Context, job JobDoc, lease Lease, m *extensions.Manifest, event string, ok bool, code string, result bson.M, f func(*ExtensionStateDoc) error) error {
	return a.fenced(ctx, lease, func(sc context.Context) error {
		payload := bson.M{"extension_id": m.ID, "ok": ok, "code": optStr(code)}
		if e := a.extApply(sc, job.OrgID, m, event, payload, job.OperationID, f); e != nil {
			return e
		}
		if ok {
			if e := a.transitionOperation(sc, job, "succeeded", result); e != nil {
				return e
			}
		} else if e := a.transitionOperation(sc, job, "failed", bson.M{"code": code}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}

func (a *App) extJobTarget(ctx context.Context, job JobDoc) (*extensions.Manifest, ExtensionStateDoc, error) {
	id, _ := job.Payload["extension_id"].(string)
	m, ok := extensions.Get(id)
	if !ok {
		return nil, ExtensionStateDoc{}, &JobFailure{Code: "extension_unknown", Message: "unknown extension"}
	}
	d, err := a.extState(ctx, job.OrgID, id)
	return m, d, err
}

// runExtensionDetect hashes the registered path and asks the binary for its version.
func (a *App) runExtensionDetect(ctx context.Context, job JobDoc, lease Lease) error {
	m, d, err := a.extJobTarget(ctx, job)
	if err != nil {
		return err
	}
	fail := func(code string) error {
		return a.extFinish(ctx, job, lease, m, "extension.detected", false, code, nil, func(n *ExtensionStateDoc) error {
			if n.Path != d.Path {
				return errExtSkip
			}
			if nx, e := extensions.Next(extensions.Status(n.State), extensions.ActDetectFail); e == nil {
				n.State = string(nx)
			}
			n.LastError, n.Detected = code, nil
			return nil
		})
	}
	sum, herr := sha256File(d.Path)
	if herr != nil {
		return fail("binary_missing")
	}
	permit, why := extensions.Probe(m, extSnapshot(d), sum)
	if why != "" {
		return fail(why)
	}
	res := a.extExec(ctx, permit, job.OrgID, m.Detect.Args, nil)
	if res.Err != nil || res.TimedOut || res.Exit != 0 {
		return fail("detect_failed")
	}
	ver, found := semver.Find(string(res.Stdout))
	if !found {
		return fail("version_unparseable")
	}
	if !ver.AtLeast(m.MinVersion()) {
		return fail("version_too_old")
	}
	now := time.Now().UTC()
	return a.extFinish(ctx, job, lease, m, "extension.detected", true, "", bson.M{"version": ver.String(), "sha256": sum}, func(n *ExtensionStateDoc) error {
		if n.Path != d.Path {
			return errExtSkip // the path changed while the job ran
		}
		nx, e := extensions.Next(extensions.Status(n.State), extensions.ActDetectOK)
		if e != nil {
			return errExtSkip
		}
		n.State, n.Detected, n.LastError = string(nx), &ExtDetected{Version: ver.String(), SHA256: sum, At: now}, ""
		n.LastTest, n.Health = nil, ExtHealth{Status: "ok", At: now}
		return nil
	})
}

// runExtensionTest runs the manifest healthcheck against the detected binary and records an immutable install row.
func (a *App) runExtensionTest(ctx context.Context, job JobDoc, lease Lease) error {
	m, d, err := a.extJobTarget(ctx, job)
	if err != nil {
		return err
	}
	changed := func() error {
		return a.extFinish(ctx, job, lease, m, "extension.tested", false, "extension_binary_changed", nil, func(n *ExtensionStateDoc) error {
			if nx, e := extensions.Next(extensions.Status(n.State), extensions.ActChanged); e == nil {
				n.State = string(nx)
			}
			n.Health = ExtHealth{Status: "changed", Code: "extension_binary_changed", At: time.Now().UTC()}
			return nil
		})
	}
	if d.Detected == nil {
		return a.extFinish(ctx, job, lease, m, "extension.tested", false, "extension_not_detected", nil, func(*ExtensionStateDoc) error { return errExtSkip })
	}
	sum, herr := sha256File(d.Path)
	if herr != nil || sum != d.Detected.SHA256 {
		return changed()
	}
	permit, why := extensions.Probe(m, extSnapshot(d), sum)
	if why != "" {
		return a.extFinish(ctx, job, lease, m, "extension.tested", false, why, nil, func(*ExtensionStateDoc) error { return errExtSkip })
	}
	res := a.extExec(ctx, permit, job.OrgID, m.Healthcheck.Args, nil)
	now := time.Now().UTC()
	if res.Err != nil || res.TimedOut || res.Exit != 0 {
		return a.extFinish(ctx, job, lease, m, "extension.tested", false, "healthcheck_failed", nil, func(n *ExtensionStateDoc) error {
			if nx, e := extensions.Next(extensions.Status(n.State), extensions.ActTestFail); e == nil {
				n.State = string(nx)
			}
			n.LastTest = &ExtLastTest{OK: false, Code: "healthcheck_failed", At: now}
			return nil
		})
	}
	report := bson.M{"version": d.Detected.Version, "healthcheck_exit": 0, "capabilities": nonNilStr(m.Capabilities)}
	var installID string
	return a.fenced(ctx, lease, func(sc context.Context) error {
		last, e := findMany[ExtensionInstallDoc](sc, a.DB, "extension_installs", bson.M{"org_id": job.OrgID, "extension_id": m.ID}, bson.D{{Key: "number", Value: -1}}, 1)
		if e != nil {
			return e
		}
		number := 1
		if len(last) > 0 {
			number = last[0].Number + 1
		}
		inst := ExtensionInstallDoc{ID: newID("exi"), OrgID: job.OrgID, ExtensionID: m.ID, Number: number, Path: d.Path, Version: d.Detected.Version, SHA256: sum, CapabilityReport: report, TestedAt: now, CreatedAt: now}
		if _, e = a.DB.Coll("extension_installs").InsertOne(sc, inst); e != nil {
			return e
		}
		installID = inst.ID
		payload := bson.M{"extension_id": m.ID, "ok": true, "install_number": number}
		if e = a.extApply(sc, job.OrgID, m, "extension.tested", payload, job.OperationID, func(n *ExtensionStateDoc) error {
			if n.Path != d.Path {
				return errExtSkip
			}
			nx, ne := extensions.Next(extensions.Status(n.State), extensions.ActTestOK)
			if ne != nil {
				return errExtSkip
			}
			n.State, n.ActiveInstallID, n.LastTest = string(nx), installID, &ExtLastTest{OK: true, Report: report, At: now}
			n.Health = ExtHealth{Status: "ok", At: now}
			return nil
		}); e != nil {
			return e
		}
		if e = a.transitionOperation(sc, job, "succeeded", bson.M{"install_id": installID, "install_number": number}); e != nil {
			return e
		}
		return a.completeJob(sc, lease)
	})
}

// runExtensionHealth re-stats and re-hashes the binary. It never executes the extension. A missing or different file
// moves the extension to "changed", which makes it unusable until it is detected and tested again or rolled back.
func (a *App) runExtensionHealth(ctx context.Context, job JobDoc, lease Lease) error {
	m, d, err := a.extJobTarget(ctx, job)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	status, code := "ok", ""
	sum, herr := "", error(nil)
	if d.Path == "" {
		status, code = "failed", "not_registered"
	} else if sum, herr = sha256File(d.Path); herr != nil {
		status, code = "missing", "binary_missing"
	} else if d.Detected != nil && sum != d.Detected.SHA256 {
		status, code = "changed", "extension_binary_changed"
	}
	return a.extFinish(ctx, job, lease, m, "extension.health", status == "ok", code, bson.M{"status": status}, func(n *ExtensionStateDoc) error {
		if n.Path != d.Path {
			return errExtSkip
		}
		n.Health = ExtHealth{Status: status, Code: code, At: now}
		if status == "changed" || status == "missing" {
			if nx, e := extensions.Next(extensions.Status(n.State), extensions.ActChanged); e == nil {
				n.State = string(nx)
			}
		}
		return nil
	})
}

// sweepExtensionHealth queues a health check for every enabled extension about once a sweep. It never executes
// anything itself.
func (a *App) sweepExtensionHealth(ctx context.Context) error {
	rows, err := findMany[ExtensionStateDoc](ctx, a.DB, "extension_states", bson.M{"state": string(extensions.StatusEnabled)}, bson.D{{Key: "_id", Value: 1}}, 500)
	if err != nil {
		return err
	}
	for _, d := range rows {
		if time.Since(d.Health.At) < 10*time.Minute {
			continue
		}
		org, id := d.OrgID, d.ExtensionID
		if err = a.txnRetryJob(ctx, func(sc context.Context) error {
			return a.ensureJobTx(sc, org, "extension_health", "extension_health:"+org+":"+id, 2, bson.M{"extension_id": id})
		}); err != nil {
			return err
		}
	}
	return nil
}
