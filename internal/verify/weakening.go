package verify

import (
	"path"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"orqen/internal/gitdiff"
)

// DefaultTestGlobs name the files that count as tests.
var DefaultTestGlobs = []string{"**/*_test.go", "**/*.test.*", "**/*.spec.*", "**/test/**", "**/tests/**", "**/__tests__/**", "*_test.go", "*.test.*", "*.spec.*", "test/**", "tests/**"}

// Signal is one sign that tests were weakened.
type Signal struct{ Code, Path, Detail string }

var testCase = regexp.MustCompile(`func Test|\bit(?:\.only|\.skip)?\(|\btest(?:\.only|\.skip)?\(|\bdescribe(?:\.only|\.skip)?\(|def test_|@Test\b`)
var skipMarker = regexp.MustCompile(`t\.Skip|\.skip\(|\bxit\(|\bxdescribe\(|\bit\.only|\.only\(|@pytest\.mark\.skip|@Disabled\b`)
var configFile = regexp.MustCompile(`(^|/)(Makefile|makefile|vitest\.config\.[a-z]+|jest\.config\.[a-z]+|pytest\.ini|tox\.ini|\.gitlab-ci\.yml)$`)
var scriptLine = regexp.MustCompile(`"(scripts|test|lint|check|build|ci)[a-zA-Z:_-]*"\s*:`)

func isTest(p string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, p); ok {
			return true
		}
	}
	return false
}

// Weakening inspects a diff for signs that tests were deleted, reduced, skipped or bypassed.
func Weakening(d gitdiff.Diff, globs []string, commands []Command) []Signal {
	if len(globs) == 0 {
		globs = DefaultTestGlobs
	}
	var out []Signal
	targets := map[string]bool{}
	for _, c := range commands {
		for _, a := range c.Argv {
			if strings.Contains(a, "/") || strings.Contains(a, ".") {
				targets[path.Clean(a)] = true
			}
		}
	}
	for _, f := range d.Files {
		p := f.Path
		if f.Status == "deleted" && isTest(p, globs) {
			out = append(out, Signal{"test_deleted", p, "a test file was deleted"})
		}
		if isTest(p, globs) && !f.Binary {
			removed, added, skips := 0, 0, 0
			for _, h := range f.Hunks {
				for _, l := range h.Lines {
					switch l.Kind {
					case '-':
						if testCase.MatchString(l.Text) {
							removed++
						}
					case '+':
						if testCase.MatchString(l.Text) {
							added++
						}
						if skipMarker.MatchString(l.Text) {
							skips++
						}
					}
				}
			}
			if f.Status != "deleted" && removed > added {
				out = append(out, Signal{"test_count_decreased", p, "fewer test cases than before"})
			}
			if skips > 0 {
				out = append(out, Signal{"skip_marker_added", p, "a skip or only marker was added"})
			}
		}
		if configFile.MatchString(p) {
			out = append(out, Signal{"test_config_changed", p, "build or test configuration changed"})
		} else if path.Base(p) == "package.json" {
			for _, h := range f.Hunks {
				hit := false
				for _, l := range h.Lines {
					if l.Kind != ' ' && scriptLine.MatchString(l.Text) {
						hit = true
					}
				}
				if hit {
					out = append(out, Signal{"test_config_changed", p, "package.json scripts changed"})
					break
				}
			}
		}
		if targets[path.Clean(p)] {
			out = append(out, Signal{"command_target_changed", p, "a file an approved command runs was changed"})
		}
	}
	return out
}
