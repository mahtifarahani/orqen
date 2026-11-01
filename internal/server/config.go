package server

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const Version = "0.1.0"
const SchemaVersion = 2
const DefaultMongoURI = "mongodb://127.0.0.1:27018/?replicaSet=orqen-rs&directConnection=true"
const NotReplicaSet = "MongoDB is not a replica set; transactions are required (see scripts/dev-mongo.sh)"

type Config struct {
	Port                                                  int
	MongoURI, DBName, DataDir, CredentialStore, LogFormat string
	Dev                                                   bool
	DevOrigins                                            []string
	LeaseTTL                                              time.Duration
	WorkerConcurrency                                     int
	ArtifactMaxBytes                                      int64
	CatherdBin, CatherdPath, PersonalCatherdHome          string
	// CodexBin is the codex executable (ORQEN_CODEX_BIN); CodingSlots is the system-wide number of runs that may code at once.
	CodexBin    string
	CodingSlots int
	// RestoreFrom, RestoreDryRun and RestoreYes are the flags of `orqen restore`.
	RestoreFrom               string
	RestoreDryRun, RestoreYes bool
	// SchedTick and SchedSweep tune the intake scheduler (zero means 5 s and 60 s); only tests set them.
	SchedTick, SchedSweep time.Duration
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".local", "share")
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dir = d
	}
	if runtime.GOOS == "darwin" {
		dir = filepath.Join(home, "Library", "Application Support")
	}
	return Config{Port: 4280, MongoURI: DefaultMongoURI, DBName: "orqen", DataDir: filepath.Join(dir, "Orqen"), CredentialStore: "keychain", LogFormat: "pretty", LeaseTTL: 30 * time.Second, WorkerConcurrency: 4, ArtifactMaxBytes: 67108864, CodingSlots: 1}
}
func ParseConfig(args []string) (Config, string, error) {
	c := DefaultConfig()
	fs := flag.NewFlagSet("orqen", flag.ContinueOnError)
	env := func(key, fallback string) string {
		if v, ok := os.LookupEnv("ORQEN_" + key); ok {
			return v
		}
		return fallback
	}
	intEnv := func(key string, fallback int64) (int64, error) {
		return strconv.ParseInt(env(key, strconv.FormatInt(fallback, 10)), 10, 64)
	}
	for _, pair := range []struct {
		key string
		dst *string
	}{{"MONGO_URI", &c.MongoURI}, {"DB_NAME", &c.DBName}, {"DATA_DIR", &c.DataDir}, {"CREDENTIAL_STORE", &c.CredentialStore}, {"LOG_FORMAT", &c.LogFormat}, {"CATHERD_BIN", &c.CatherdBin}, {"CODEX_BIN", &c.CodexBin}, {"CATHERD_PATH", &c.CatherdPath}, {"PERSONAL_CATHERD_HOME", &c.PersonalCatherdHome}} {
		*pair.dst = env(pair.key, *pair.dst)
	}
	port, err := intEnv("PORT", int64(c.Port))
	if err != nil {
		return c, "", err
	}
	c.Port = int(port)
	workers, err := intEnv("WORKER_CONCURRENCY", int64(c.WorkerConcurrency))
	if err != nil {
		return c, "", err
	}
	c.WorkerConcurrency = int(workers)
	slots, err := intEnv("CODING_SLOTS", int64(c.CodingSlots))
	if err != nil {
		return c, "", err
	}
	c.CodingSlots = int(slots)
	maxBytes, err := intEnv("ARTIFACT_MAX_BYTES", c.ArtifactMaxBytes)
	if err != nil {
		return c, "", err
	}
	c.ArtifactMaxBytes = maxBytes
	lease, err := intEnv("LEASE_TTL_SECS", 30)
	if err != nil {
		return c, "", err
	}
	c.Dev, err = strconv.ParseBool(env("DEV", "false"))
	if err != nil {
		return c, "", err
	}
	origins := env("DEV_ORIGINS", "")
	fs.IntVar(&c.Port, "port", c.Port, "loopback port")
	fs.StringVar(&c.MongoURI, "mongo-uri", c.MongoURI, "MongoDB replica set URI")
	fs.StringVar(&c.DBName, "db-name", c.DBName, "database name")
	fs.StringVar(&c.DataDir, "data-dir", c.DataDir, "data directory")
	fs.StringVar(&c.CredentialStore, "credential-store", c.CredentialStore, "keychain or memory")
	fs.BoolVar(&c.Dev, "dev", c.Dev, "allow development origins and read UI from web/dist")
	fs.StringVar(&origins, "dev-origins", origins, "comma-separated development origins")
	fs.Int64Var(&lease, "lease-ttl-secs", lease, "job lease TTL")
	fs.IntVar(&c.WorkerConcurrency, "worker-concurrency", c.WorkerConcurrency, "concurrent jobs")
	fs.StringVar(&c.CatherdBin, "catherd-bin", c.CatherdBin, "path of the catherd executable")
	fs.StringVar(&c.CodexBin, "codex-bin", c.CodexBin, "path of the codex executable")
	fs.IntVar(&c.CodingSlots, "coding-slots", c.CodingSlots, "runs that may code at once, system-wide")
	fs.StringVar(&c.CatherdPath, "catherd-path", c.CatherdPath, "PATH catherd runs with")
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "pretty or json")
	fs.Int64Var(&c.ArtifactMaxBytes, "artifact-max-bytes", c.ArtifactMaxBytes, "maximum artifact size")
	fs.StringVar(&c.RestoreFrom, "from", "", "restore: the backup directory")
	fs.BoolVar(&c.RestoreDryRun, "dry-run", false, "restore: print the preview and write nothing")
	fs.BoolVar(&c.RestoreYes, "yes", false, "restore: do not ask for confirmation")
	command := ""
	var flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "serve" || a == "migrate" || a == "login-link" || a == "openapi" || a == "restore" {
			if command != "" {
				return c, "", fmt.Errorf("multiple commands")
			}
			command = a
			continue
		}
		flags = append(flags, a)
		if strings.HasPrefix(a, "--") && !strings.Contains(a, "=") && a != "--dev" && a != "--help" && a != "--dry-run" && a != "--yes" {
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	if err := fs.Parse(flags); err != nil {
		return c, command, err
	}
	if len(fs.Args()) > 0 {
		return c, command, fmt.Errorf("unknown arguments: %v", fs.Args())
	}
	if command == "" {
		return c, command, fmt.Errorf("usage: orqen [options] serve|openapi|login-link|migrate|restore")
	}
	if c.Port < 0 || c.Port > 65535 || lease < 1 || c.WorkerConcurrency < 1 || c.CodingSlots < 1 || c.CodingSlots > 8 || c.ArtifactMaxBytes < 1 {
		return c, command, fmt.Errorf("port, lease TTL, worker concurrency or artifact limit is out of range")
	}
	if command == "restore" && c.RestoreFrom == "" {
		return c, command, fmt.Errorf("restore needs --from <backup directory>")
	}
	if c.CredentialStore != "memory" && c.CredentialStore != "keychain" {
		return c, command, fmt.Errorf("credential-store must be keychain or memory")
	}
	if c.LogFormat != "pretty" && c.LogFormat != "json" {
		return c, command, fmt.Errorf("log-format must be pretty or json")
	}
	c.LeaseTTL = time.Duration(lease) * time.Second
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.DevOrigins = append(c.DevOrigins, o)
		}
	}
	return c, command, nil
}
func (c Config) devOrigins() []string {
	if !c.Dev {
		return nil
	}
	if len(c.DevOrigins) == 0 {
		return []string{"http://localhost:5173"}
	}
	return c.DevOrigins
}
func (c Config) AllowedOrigins() []string {
	r := []string{fmt.Sprintf("http://127.0.0.1:%d", c.Port), fmt.Sprintf("http://localhost:%d", c.Port)}
	for _, o := range c.devOrigins() {
		r = append(r, strings.TrimRight(o, "/"))
	}
	return r
}
func (c Config) AllowedHosts() []string {
	r := []string{fmt.Sprintf("127.0.0.1:%d", c.Port), fmt.Sprintf("localhost:%d", c.Port), fmt.Sprintf("[::1]:%d", c.Port)}
	for _, o := range c.devOrigins() {
		if _, h, ok := strings.Cut(o, "://"); ok {
			r = append(r, strings.TrimRight(h, "/"))
		}
	}
	return r
}
func (c Config) CookieName() string { return fmt.Sprintf("orqen_session_%d", c.Port) }
