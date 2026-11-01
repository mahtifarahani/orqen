// Package canon renders values as canonical JSON (sorted keys, no HTML escaping, exact numbers).
package canon

import (
	"encoding/json"
	"strings"
)

func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err = dec.Decode(&value); err != nil {
		panic(err)
	}
	var out strings.Builder
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(value); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(out.String(), "\n")
}
