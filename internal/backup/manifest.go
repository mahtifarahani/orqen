package backup

import (
	"regexp"
	"time"
)

// Format identifiers.
const (
	FormatName    = "orqen-backup"
	FormatVersion = 1
)

// NamePattern matches the name of a backup directory.
var NamePattern = regexp.MustCompile(`^orqen-backup-\d{8}T\d{6}Z-[0-9a-f]{8}$`)

// Names of the fixed files.
const (
	ManifestFile  = "manifest.json"
	ManifestSum   = "MANIFEST.sha256"
	DBDir         = "db"
	ArtifactsDir  = "artifacts"
	PartialPrefix = ".partial-"
)

// Org identifies an organization in the manifest.
type Org struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// CollectionEntry describes one db/<collection>.jsonl.gz file.
type CollectionEntry struct {
	Name   string `json:"name"`
	Docs   int64  `json:"docs"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ArtifactsEntry summarizes the copied blobs.
type ArtifactsEntry struct {
	Count      int    `json:"count"`
	Bytes      int64  `json:"bytes"`
	ListSHA256 string `json:"list_sha256"`
}

// Excluded records what the backup deliberately leaves out.
type Excluded struct {
	Collections []string `json:"collections"`
	DataDirs    []string `json:"data_dirs"`
}

// ExcludedArtifact is a blob left out because a secret pattern matched (backup.on_artifact_pattern=exclude_artifact).
type ExcludedArtifact struct {
	ArtifactID  string `json:"artifact_id"`
	PatternCode string `json:"pattern_code"`
}

// SecretScan records what the scanner did.
type SecretScan struct {
	KnownValuesChecked int                `json:"known_values_checked"`
	PatternsVersion    string             `json:"patterns_version"`
	ExcludedArtifacts  []ExcludedArtifact `json:"excluded_artifacts"`
}

// Manifest is manifest.json.
type Manifest struct {
	Format                string            `json:"format"`
	FormatVersion         int               `json:"format_version"`
	OrqenVersion          string            `json:"orqen_version"`
	SchemaVersion         int               `json:"schema_version"`
	PolicyDefaultsVersion int               `json:"policy_defaults_version"`
	CreatedAt             time.Time         `json:"created_at"`
	ClusterTime           string            `json:"cluster_time"`
	Orgs                  []Org             `json:"orgs"`
	Collections           []CollectionEntry `json:"collections"`
	Artifacts             ArtifactsEntry    `json:"artifacts"`
	Excluded              Excluded          `json:"excluded"`
	SecretScan            SecretScan        `json:"secret_scan"`
}

// BlobPath is the path of a blob below the backup's artifacts directory (and below <data>/artifacts).
func BlobPath(org, digest string) string {
	if len(digest) < 4 {
		return ArtifactsDir + "/" + org + "/sha256/" + digest
	}
	return ArtifactsDir + "/" + org + "/sha256/" + digest[:2] + "/" + digest[2:4] + "/" + digest
}

// CollectionFile is the path of a collection file below the backup directory.
func CollectionFile(name string) string { return DBDir + "/" + name + ".jsonl.gz" }

// NewName builds the directory name of a backup from the clock and eight random hex digits.
func NewName(now time.Time, hex8 string) string {
	return "orqen-backup-" + now.UTC().Format("20060102T150405Z") + "-" + hex8
}
