package server

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// versionDTO is KnowledgeVersionDto. The body is omitted from list views when full is false.
func (a *App) versionDTO(ctx context.Context, org string, v KnowledgeVersionDoc, _ any) bson.M {
	claims := []bson.M{}
	for _, c := range v.Claims {
		ev := []bson.M{}
		for _, e := range c.Evidence {
			ev = append(ev, bson.M{"ref_kind": e.RefKind, "ref_id": e.RefID, "locator": e.Locator, "excerpt": e.Excerpt, "label": a.evidenceLabel(ctx, org, e)})
		}
		claims = append(claims, bson.M{"id": c.ID, "text": c.Text, "template": c.Template, "fact": c.Fact, "critical": c.Critical, "verdict": string(c.Verdict), "verdict_by": string(c.VerdictBy), "evidence": ev})
	}
	var prom any
	if v.Promotion != nil {
		atts := []bson.M{}
		for _, t := range v.Promotion.Attestations {
			atts = append(atts, bson.M{"claim_id": t.ClaimID, "text": t.Text, "by": t.By, "at": timestamp(t.At)})
		}
		prom = bson.M{"mode": v.Promotion.Mode, "decided_by": v.Promotion.DecidedBy, "policy_version_id": optStr(v.Promotion.PolicyVersionID), "at": timestamp(v.Promotion.At), "note": v.Promotion.Note, "attestations": atts}
	}
	var vf, vu, va any
	if v.ValidFrom != nil {
		vf = timestamp(*v.ValidFrom)
	}
	if v.ValidUntil != nil {
		vu = timestamp(*v.ValidUntil)
	}
	if v.VerifiedAt != nil {
		va = timestamp(*v.VerifiedAt)
	}
	m := bson.M{"id": v.ID, "record_id": v.RecordID, "number": v.Number, "type": v.Type, "scope_key": v.ScopeKey, "title": v.Title, "body": v.Body, "claims": claims,
		"valid_from": vf, "valid_until": vu, "status": v.Status, "supersedes": optStr(v.Supersedes), "superseded_by": optStr(v.SupersededBy), "rollback_of": optStr(v.RollbackOf),
		"origin": v.Origin, "promotion": prom, "evidence_stale": v.EvidenceStale, "injection_flags": nonNilStrings(v.InjectionFlags), "content_hash": v.ContentHash,
		"verified_at": va, "created_at": timestamp(v.CreatedAt)}
	if len(v.RoleVerdicts) > 0 {
		rv := []bson.M{}
		for _, r := range v.RoleVerdicts {
			rv = append(rv, bson.M{"claim_id": r.ClaimID, "verdict": r.Verdict, "reason": r.Reason})
		}
		m["role_verdicts"] = rv
	}
	return m
}

func (a *App) conflictDTO(c KnowledgeConflictDoc) bson.M {
	pairs := []bson.M{}
	for _, p := range c.Pairs {
		pairs = append(pairs, bson.M{"a_version": p.AVersion, "b_version": p.BVersion, "kind": p.Kind, "a_claim": p.AClaim, "b_claim": p.BClaim})
	}
	var res any
	if c.Resolution != nil {
		res = bson.M{"choice": c.Resolution.Choice, "note": c.Resolution.Note, "by": c.Resolution.By, "at": timestamp(c.Resolution.At), "result_version_id": optStr(c.Resolution.ResultVersionID)}
	}
	return bson.M{"id": c.ID, "version_ids": nonNilStrings(c.VersionIDs), "record_ids": nonNilStrings(c.RecordIDs), "pairs": pairs, "state": c.State, "resolution": res,
		"revision": c.Revision, "created_at": timestamp(c.CreatedAt), "updated_at": timestamp(c.UpdatedAt)}
}
