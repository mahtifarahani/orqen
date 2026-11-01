package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"orqen/api"
	"orqen/internal/server"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func main() { os.Exit(run(os.Args[1:])) }
func run(args []string) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Println("orqen " + server.Version)
		return 0
	}
	config, command, err := server.ParseConfig(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if command == "openapi" {
		fmt.Print(string(api.OpenAPI))
		return 0
	}
	var handler slog.Handler
	if config.LogFormat == "json" {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	} else {
		handler = slog.NewTextHandler(os.Stderr, nil)
	}
	slog.SetDefault(slog.New(handler))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := server.OpenDB(ctx, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var newer *server.SchemaTooNew
		if errors.As(err, &newer) {
			return 3
		}
		if err.Error() == server.NotReplicaSet {
			return 2
		}
		return 1
	}
	defer db.Client.Disconnect(context.Background())
	switch command {
	case "migrate":
		fmt.Printf("schema version %d\n", server.SchemaVersion)
		return 0
	case "login-link":
		exists, e := db.OwnerExists(ctx)
		if e != nil {
			err = e
		} else if !exists {
			err = fmt.Errorf("no owner yet; open the bootstrap link printed by `orqen serve`")
		} else {
			var raw string
			raw, err = db.IssueToken(ctx, "login")
			if err == nil {
				fmt.Printf("http://127.0.0.1:%d/login#token=%s\n", config.Port, raw)
			}
		}
	case "restore":
		return runRestore(ctx, config, db)
	case "serve":
		var listener net.Listener
		listener, err = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(config.Port)))
		if err != nil {
			break
		}
		config.Port = listener.Addr().(*net.TCPAddr).Port
		var creds server.CredentialStore = server.KeychainStore{}
		if config.CredentialStore == "memory" {
			slog.Warn("using the in-memory credential store; secrets are lost on restart")
			creds = server.NewMemoryStore()
		}
		app := server.NewApp(config, db, creds)
		app.InitCatherd(ctx)
		app.UseCodexHost()
		slog.Info("orqen listening on 127.0.0.1", "port", config.Port)
		err = app.Run(ctx, listener, true)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// runRestore is `orqen restore --from <dir> [--dry-run] [--yes]`: it prints the JSON preview and, unless --dry-run,
// asks for confirmation before restoring into this (empty) install. The API never restores.
func runRestore(ctx context.Context, config server.Config, db *server.DB) int {
	var creds server.CredentialStore = server.KeychainStore{}
	if config.CredentialStore == "memory" {
		creds = server.NewMemoryStore()
	}
	app := server.NewApp(config, db, creds)
	app.InitCatherd(ctx)
	fail := func(err error) int {
		var re *server.RestoreError
		if errors.As(err, &re) {
			out, _ := json.MarshalIndent(re, "", "  ")
			fmt.Fprintln(os.Stderr, string(out))
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return 1
	}
	res, err := app.RestoreFromDir(ctx, config.RestoreFrom, true)
	if err != nil {
		return fail(err)
	}
	out, _ := json.MarshalIndent(res.Preview, "", "  ")
	fmt.Println(string(out))
	if config.RestoreDryRun {
		return 0
	}
	if !res.Preview.CanRestore {
		return fail(&server.RestoreError{Code: "restore_not_possible", Detail: strings.Join(res.Preview.Notes, ",")})
	}
	if !config.RestoreYes {
		fmt.Fprint(os.Stderr, "Restore this backup into this empty install? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(os.Stderr, "not restored")
			return 1
		}
	}
	res, err = app.RestoreFromDir(ctx, config.RestoreFrom, false)
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(os.Stderr, "restored; automation is paused and runs are held. Start `orqen serve`, run `orqen login-link`, set missing credentials and resume.")
	return 0
}
