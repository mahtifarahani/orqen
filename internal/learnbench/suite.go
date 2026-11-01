// Package learnbench is the learning benchmark harness (K39). It validates the measurement harness only: the engine is a
// deterministic fake and no improvement of the product is claimed.
package learnbench

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

//go:embed fixture-v1.json
var fixtureV1 []byte

const SuiteFixtureV1 = "fixture-v1"

type Task struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Needs   []string `json:"needs"`
	Teaches []string `json:"teaches"`
}
type Suite struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Tasks       []Task `json:"tasks"`
	Hash        string `json:"-"`
}

// Load returns an embedded suite.
func Load(id string) (Suite, error) {
	if id != SuiteFixtureV1 {
		return Suite{}, fmt.Errorf("unknown benchmark suite %q", id)
	}
	var s Suite
	if err := json.Unmarshal(fixtureV1, &s); err != nil {
		return Suite{}, err
	}
	h := sha256.Sum256(fixtureV1)
	s.Hash = hex.EncodeToString(h[:])
	return s, nil
}
