package codegraph

import (
	"fmt"
	"strings"

	"orqen/internal/contextpkt"
)

// ItemKind and SourceID of the packet item.
const (
	ItemKind     = "code_context"
	ItemSourceID = "code_impact"
)

// ImpactItem renders an impact result as the code_impact packet item. The header names the commit the index was built
// for (commit=<sha12>), the text is external (never trusted as instructions), and it is cut at maxBytes with a visible
// note.
func ImpactItem(res ImpactResult, commit string, maxBytes int, orgID string) contextpkt.Item {
	c := commit
	if len(c) > 12 {
		c = c[:12]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "code impact commit=%s target=%s\n", c, oneLine(res.Target))
	note := "\n[truncated]"
	for _, it := range res.Items {
		line := fmt.Sprintf("%s %s", it.Relation, oneLine(it.Path))
		if it.Symbol != "" {
			line += " " + oneLine(it.Symbol)
		}
		line += fmt.Sprintf(" (depth %d)\n", it.Depth)
		if b.Len()+len(line)+len(note) > maxBytes {
			res.Truncated = true
			break
		}
		b.WriteString(line)
	}
	text := strings.TrimRight(b.String(), "\n")
	if res.Truncated {
		text += note
	}
	if len(text) > maxBytes && maxBytes > len(note) {
		text = strings.ToValidUTF8(text[:maxBytes-len(note)], "") + note
	}
	return contextpkt.Item{SourceID: ItemSourceID, Kind: ItemKind, Label: "code impact", Ref: c, Text: text, External: true, Trust: "external", OrgID: orgID}
}

func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
