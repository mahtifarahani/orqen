package skills

import "orqen/internal/knowledge"

// ContentSHA is the sha256 of the S11-normalized file text.
func ContentSHA(content string) string { return knowledge.SHA(knowledge.NormText(content)) }

// SHA8 is the short form shown in headers and manifests.
func SHA8(sha string) string {
	if len(sha) < 8 {
		return sha
	}
	return sha[:8]
}
