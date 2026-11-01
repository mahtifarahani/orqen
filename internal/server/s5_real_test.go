package server

import (
	"os"
	"testing"
)

// Gated real tests of Sprint 5. They never run by default and the coder never runs them: each one needs an explicit
// opt-in variable and a binary or service the user provides.

// TestWikiReal checks the ASSUMED Wiki CLI contract v1 against a real OpenClaw Memory Wiki binary.
func TestWikiReal(t *testing.T) {
	bin := os.Getenv("ORQEN_TEST_WIKI_BIN")
	if bin == "" {
		t.Skip("set ORQEN_TEST_WIKI_BIN to a Wiki CLI binary to verify contract v1 (never run by the coder)")
	}
	t.Skip("contract probe not wired yet: run the kindex.CLI verbs against " + bin + " in a temp vault and diff the outputs with schema/v1")
}

// TestConfluenceReal reads one page from a real Confluence (GET only).
func TestConfluenceReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_CONFLUENCE_URL") == "" {
		t.Skip("set ORQEN_TEST_CONFLUENCE_URL (and a token in the Keychain) to read one real page (never run by the coder)")
	}
	t.Skip("real page read not wired yet")
}

// TestKnowledgeAuthorRolesReal drives the writer and verifier roles through real catherd (token spend).
func TestKnowledgeAuthorRolesReal(t *testing.T) {
	if os.Getenv("ORQEN_TEST_CATHERD_BIN") == "" || os.Getenv("ORQEN_TEST_SPEND_OK") != "yes" {
		t.Skip("needs ORQEN_TEST_CATHERD_BIN and ORQEN_TEST_SPEND_OK=yes (real model spend; never run by the coder)")
	}
	t.Skip("roles author is not built in this sprint")
}
