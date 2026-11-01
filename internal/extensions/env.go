package extensions

import (
	"fmt"
	"sort"
)

// BuildEnv returns the complete child environment: the fixed "NAME=value" entries plus the secret or config values
// for the allowlisted names. A name outside the allowlist panics (the catherd SecretEnv pattern): it is a programming
// error, never user input. Nothing is inherited.
func BuildEnv(fixed []string, allowed []string, values map[string]string) []string {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	out := append([]string{}, fixed...)
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !ok[n] {
			panic(fmt.Sprintf("extensions: environment name %q is not in the allowlist", n))
		}
		out = append(out, n+"="+values[n])
	}
	return out
}
