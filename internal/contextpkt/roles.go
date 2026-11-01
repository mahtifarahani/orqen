package contextpkt

// Role names of the per-role packets (K19).
const (
	RoleArchitect = "architect"
	RoleWorker    = "worker"
	RoleVerifier  = "verifier"
	RoleReviewer  = "reviewer"
	RoleWriter    = "writer"
)

// RoleProfiles lists the sections each role receives. The reviewer never gets task_discussion (and the server never
// puts a worker trace into any packet).
var RoleProfiles = map[string][]string{
	RoleArchitect: SectionOrder,
	RoleWorker:    SectionOrder,
	RoleVerifier:  {SecCriticalRules, SecGoal, SecRepoRules, SecCode, SecKnowledge},
	RoleReviewer:  {SecCriticalRules, SecGoal, SecRepoRules, SecDesignRefs, SecCode, SecKnowledge, SecExperience},
	RoleWriter:    {SecCriticalRules, SecGoal, SecEvidence},
}

// BuildRole builds the packet of one role from the same sections as the run packet: sections outside the role
// profile are left out, critical items not meant for the role are excluded with not_for_role, and a critical set
// that does not fit still returns Oversize (with Role set), never a truncated rule.
func BuildRole(in In, role string) (Packet, Manifest, *Oversize) {
	allowed := map[string]bool{}
	for _, id := range RoleProfiles[role] {
		allowed[id] = true
	}
	out := in
	out.Role = role
	out.Sections = nil
	for _, sec := range in.Sections {
		if allowed[sec.ID] {
			out.Sections = append(out.Sections, sec)
		}
	}
	return Build(out)
}
