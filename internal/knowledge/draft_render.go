package knowledge

import (
	"fmt"
	"strings"
)

// RenderDraftForVerify renders the writer's drafts for the knowledge verifier. Claim ids are "<key>/c<n>", unique
// across the whole reply.
func RenderDraftForVerify(recs []WriterRecord) string {
	var sb strings.Builder
	for _, r := range recs {
		fmt.Fprintf(&sb, "record %s (%s): %s\n", r.Key, r.Type, oneLine(r.Title, 200))
		for _, c := range r.Claims {
			fmt.Fprintf(&sb, "  claim_id=%s/%s: %s\n", r.Key, c.ID, oneLine(c.Text, MaxClaimText))
			for _, e := range c.Evidence {
				fmt.Fprintf(&sb, "    cites ref_id=%s: %s\n", e.RefID, oneLine(e.Excerpt, MaxExcerpt))
			}
		}
	}
	return sb.String()
}

// SplitVerdicts groups the verifier's verdicts by record key and maps the claim ids back to "c<n>". A verdict whose
// id is not of the form "<key>/c<n>" is dropped.
func SplitVerdicts(vs []KVerdict) map[string][]KVerdict {
	out := map[string][]KVerdict{}
	for _, v := range vs {
		i := strings.LastIndex(v.ClaimID, "/")
		if i <= 0 || i == len(v.ClaimID)-1 {
			continue
		}
		key, id := v.ClaimID[:i], v.ClaimID[i+1:]
		out[key] = append(out[key], KVerdict{ClaimID: id, Verdict: v.Verdict, Reason: v.Reason})
	}
	return out
}
