// Command orqen-fakehost stands in for the Codex CLI and the catherd CLI during manual verification. It is
// development-only, never built into the orqen executable, never starts a model and never contacts a network.
//
//	orqen-fakehost app-server [flags]   the fake Codex App Server on stdio (hostfake), executing Orqen's directives
//	                                    against a file-backed fake catherd
//	orqen-fakehost catherd <args>       the catherd CLI subset Orqen uses (profiles, catalog, doctor, runs, profile use)
//	orqen-fakehost codex <args>         `codex app-server ...`, `codex --version`, `codex login status`
//	orqen-fakehost openclaw <args>      the assumed Wiki CLI contract v1 (`wiki <verb> --vault <dir> --json`), state in
//	                                    $ORQEN_FAKEHOST_DIR/wiki, scenario keys wiki.* in scenario.json
//	orqen-fakehost gitnexus <args>      the assumed GitNexus CLI contract v1 (`--version`, `analyze ...`, `query impact|context ...`),
//	                                    scenario keys gitnexus.* in scenario.json
//	orqen-fakehost strix <args>         the assumed Strix CLI contract v1 (`--version`, `--target ... --non-interactive ...`),
//	                                    scenario keys strix.* in scenario.json (scenario: findings, budget_stop_exit0, ...)
//
// State lives in $ORQEN_FAKEHOST_DIR (required): state/ for the run engine, scenario.json for the failure scenario
// (rewritten by PUT /_fakes/host/scenario on orqen-fakes), profiles.json for catherd profiles. Orqen points
// ORQEN_CODEX_BIN and ORQEN_CATHERD_BIN at two tiny wrapper scripts that exec this binary (see README, "Fakes").
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"orqen/internal/catherd"
	"orqen/internal/codegraph"
	"orqen/internal/kindex"
	"orqen/internal/secscan"
	"orqen/internal/testfakes/hostfake"
)

const codexVersion = "0.160.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: orqen-fakehost app-server|catherd|codex|openclaw|gitnexus|strix ...")
		os.Exit(2)
	}
	dir := os.Getenv("ORQEN_FAKEHOST_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "ORQEN_FAKEHOST_DIR is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "app-server":
		appServer(dir)
	case "codex":
		codex(dir, os.Args[2:])
	case "catherd":
		os.Exit(catherdCLI(dir, os.Args[2:]))
	case "openclaw":
		os.Exit(kindex.RunFakeCLI(os.Args[2:], os.Stdin, os.Stdout, filepath.Join(dir, "wiki")))
	case "gitnexus":
		os.Exit(codegraph.RunFakeCLI(os.Args[2:], os.Stdout, filepath.Join(dir, "scenario.json")))
	case "strix":
		os.Exit(secscan.RunFakeCLI(os.Args[2:], os.Stdout, filepath.Join(dir, "scenario.json")))
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func runEngine(dir string) *hostfake.Catherd {
	// Like the real catherd, run folders live in <CATHERD_HOME>/data/runs, where Orqen's path check expects them.
	home := os.Getenv("CATHERD_HOME")
	if home == "" {
		home = filepath.Join(dir, "home")
	}
	return hostfake.NewFileCatherd(filepath.Join(home, "data"), filepath.Join(dir, "state"))
}

func appServer(dir string) {
	srv := hostfake.NewAppServer(runEngine(dir))
	srv.Serve(os.Stdin, os.Stdout)
}

func codex(dir string, args []string) {
	switch {
	case len(args) > 0 && args[0] == "app-server":
		appServer(dir)
	case len(args) > 0 && args[0] == "--version":
		fmt.Println("codex-cli " + codexVersion)
	case len(args) > 1 && args[0] == "login" && args[1] == "status":
		fmt.Println("Logged in using an API key")
	default:
		fmt.Fprintln(os.Stderr, "orqen-fakehost codex: unsupported arguments")
		os.Exit(2)
	}
}

// ---- catherd CLI ----

func catherdCLI(dir string, args []string) int {
	out := func(v any) int {
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
		return 0
	}
	fail := func(code, msg string, exit int) int {
		fmt.Fprintf(os.Stderr, "error %s: %s\n", code, msg)
		return exit
	}
	if len(args) == 0 {
		return fail("E_INPUT_INVALID", "no command", 2)
	}
	if args[0] == "--version" {
		fmt.Println("1.5.0")
		return 0
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return fail("E_INTERNAL", err.Error(), 1)
	}
	defer unlock()
	fake := catherd.NewFake()
	statePath := filepath.Join(dir, "profiles.json")
	if b, err := os.ReadFile(statePath); err == nil {
		if err = fake.ImportState(b); err != nil {
			return fail("E_INTERNAL", "bad profiles.json: "+err.Error(), 1)
		}
	}
	defer func() {
		if b, err := fake.ExportState(); err == nil {
			_ = os.WriteFile(statePath, b, 0o600)
		}
	}()
	fake.Runs = runEngine(dir)
	home := os.Getenv("CATHERD_HOME")
	env := orgEnv(home)
	ctx := context.Background()
	flag := func(name string) (string, bool) {
		for i, a := range args {
			if a == name && i+1 < len(args) {
				return args[i+1], true
			}
		}
		return "", false
	}
	has := func(name string) bool {
		for _, a := range args {
			if a == name {
				return true
			}
		}
		return false
	}
	host, _ := flag("--host")
	if host == "" {
		host = catherd.HostCodex
	}
	apiErr := func(err error) int {
		var ce *catherd.Error
		if e, ok := err.(*catherd.Error); ok {
			ce = e
		}
		if ce == nil {
			return fail("E_INTERNAL", err.Error(), 1)
		}
		code := ce.Code
		if !strings.HasPrefix(code, "E_") {
			code = "E_INTERNAL"
		}
		exit := ce.Exit
		if exit == 0 {
			exit = 1
		}
		return fail(code, ce.Message, exit)
	}
	switch args[0] {
	case "profile":
		if len(args) < 2 {
			return fail("E_INPUT_INVALID", "profile needs a subcommand", 2)
		}
		switch args[1] {
		case "list":
			rows, err := fake.ListProfiles(ctx, env)
			if err != nil {
				return apiErr(err)
			}
			return out(rows)
		case "show":
			if len(args) < 3 {
				return fail("E_INPUT_INVALID", "profile show needs a name", 2)
			}
			if has("--raw") {
				d, err := fake.ShowRaw(ctx, env, host, args[2])
				if err != nil {
					return apiErr(err)
				}
				return out(d)
			}
			d, err := fake.ShowResolved(ctx, env, host, args[2])
			if err != nil {
				return apiErr(err)
			}
			return out(d)
		case "new":
			if len(args) < 3 {
				return fail("E_INPUT_INVALID", "profile new needs a name", 2)
			}
			if err := fake.Create(ctx, env, host, args[2], nil); err != nil {
				return apiErr(err)
			}
			return 0
		case "rm":
			if len(args) < 3 {
				return fail("E_INPUT_INVALID", "profile rm needs a name", 2)
			}
			if err := fake.Remove(ctx, env, args[2]); err != nil {
				return apiErr(err)
			}
			return 0
		case "set":
			name, _ := flag("--profile")
			rest := args[2:]
			// the last two arguments are KEY and the JSON value
			if len(rest) < 2 || name == "" {
				return fail("E_INPUT_INVALID", "profile set needs --profile, a key and a value", 2)
			}
			key, raw := rest[len(rest)-2], rest[len(rest)-1]
			v, err := catherd.Decode([]byte(`{"v":` + raw + `}`))
			if err != nil {
				return fail("E_INPUT_INVALID", "the value is not JSON", 2)
			}
			res, err := fake.SetKey(ctx, env, host, name, key, v["v"], nil)
			if err != nil {
				return apiErr(err)
			}
			if len(res.Refused) > 0 {
				parts := make([]string, 0, len(res.Refused))
				for _, is := range res.Refused {
					parts = append(parts, is.PathDisplay+": "+is.Message)
				}
				return fail("E_CONFIG_INVALID", "the profile was not saved: "+strings.Join(parts, "; "), 1)
			}
			return 0
		case "validate":
			if len(args) < 3 {
				return fail("E_INPUT_INVALID", "profile validate needs a name", 2)
			}
			v, err := fake.Validate(ctx, env, host, args[2], nil)
			if err != nil {
				return apiErr(err)
			}
			conv := func(in []catherd.Issue) []map[string]string {
				res := []map[string]string{}
				for _, is := range in {
					res = append(res, map[string]string{"path": is.PathDisplay, "message": is.Message, "fix": is.Fix})
				}
				return res
			}
			out(map[string]any{"valid": v.Valid, "errors": conv(v.Errors), "warnings": conv(v.Warnings)})
			if !v.Valid {
				return 1
			}
			return 0
		case "use":
			if len(args) < 3 || !has("--repo") {
				return fail("E_INPUT_INVALID", "profile use needs a name and --repo", 2)
			}
			cwd, err := os.Getwd()
			if err != nil {
				return fail("E_INTERNAL", err.Error(), 1)
			}
			if err := fake.ProfileUseRepo(ctx, env, host, args[2], cwd); err != nil {
				return apiErr(err)
			}
			return 0
		}
	case "catalog":
		if len(args) < 2 {
			return fail("E_INPUT_INVALID", "catalog needs a subcommand", 2)
		}
		switch args[1] {
		case "list":
			b, err := fake.CatalogList(ctx, env)
			if err != nil {
				return apiErr(err)
			}
			fmt.Println(string(b))
			return 0
		case "refresh":
			b, err := fake.CatalogRefresh(ctx, env, nil)
			if err != nil {
				return apiErr(err)
			}
			fmt.Println(string(b))
			return 0
		case "sync":
			b, err := fake.CatalogSync(ctx, env, has("--force"), nil)
			if err != nil {
				return apiErr(err)
			}
			fmt.Println(string(b))
			return 0
		}
	case "doctor":
		b, exit, err := fake.Doctor(ctx, env, host, nil)
		if err != nil {
			return apiErr(err)
		}
		fmt.Println(string(b))
		return exit
	case "runs":
		if len(args) >= 3 && args[1] == "show" {
			v, err := fake.RunsShow(ctx, env, args[2])
			if err != nil {
				return apiErr(err)
			}
			return out(runViewJSON(v))
		}
		if len(args) >= 4 && args[1] == "cancel" {
			if err := fake.RunsCancel(ctx, env, args[2], args[3]); err != nil {
				return apiErr(err)
			}
			return 0
		}
	}
	return fail("E_INPUT_INVALID", "unsupported catherd command: "+strings.Join(args, " "), 2)
}

// orgEnv rebuilds the organization's namespace from CATHERD_HOME (<data>/catherd/<org>/home).
func orgEnv(home string) *catherd.OrgEnv {
	root := filepath.Dir(home)
	return catherd.NewOrgEnv(filepath.Dir(filepath.Dir(root)), filepath.Base(root))
}

// runViewJSON is the shape of `catherd runs show --json --debug` that catherd.ParseRunView reads.
func runViewJSON(v catherd.RunView) map[string]any {
	norm := func(r *catherd.RunRecord) {
		if r.ChangedOwned == nil {
			r.ChangedOwned = []string{}
		}
		if r.Violations == nil {
			r.Violations = []string{}
		}
	}
	for i := range v.Records {
		norm(&v.Records[i])
	}
	disp := []map[string]any{}
	for _, d := range v.Dispatches {
		if d.Record != nil {
			norm(d.Record)
		}
		m := map[string]any{"name": d.Name, "dispatchId": d.DispatchID, "rung": d.Rung, "admittedAt": d.AdmittedAt}
		if d.ExitReason != "" {
			m["exit"] = map[string]string{"reason": d.ExitReason}
		}
		if d.Record != nil {
			m["record"] = d.Record
		}
		disp = append(disp, m)
	}
	recs := v.Records
	if recs == nil {
		recs = []catherd.RunRecord{}
	}
	miles := v.Landed
	if miles == nil {
		miles = []string{}
	}
	sort.Strings(miles)
	return map[string]any{"summary": map[string]any{"milestones": miles}, "records": recs, "dispatches": disp}
}

func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, "catherd.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
