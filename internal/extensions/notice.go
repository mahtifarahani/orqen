package extensions

// Notice is the Orqen-written summary shown before a license acceptance, in one locale. It is a summary plus a link
// label, never the upstream license text. The server accepts an acceptance only when the client sends the digest of
// one of these exact texts, so the person provably saw the current notice.
type Notice struct {
	Locale string `json:"locale"`
	Text   string `json:"text"`
	SHA256 string `json:"sha256"`
}

var noticeTexts = map[string]map[string]string{
	"gitnexus": {
		"en": "GitNexus is distributed under the PolyForm Noncommercial 1.0.0 license. Orqen does not bundle, download or install it. Use it only if your use is noncommercial or you hold a commercial license from its authors. Read the license at the project page before you accept. Accepting is recorded with your name, basis and note, and you can revoke it at any time.",
		"fa": "GitNexus با مجوز PolyForm Noncommercial 1.0.0 منتشر می‌شود. Orqen آن را همراه خود نمی‌آورد، دانلود یا نصب نمی‌کند. فقط در صورتی از آن استفاده کنید که کاربرد شما غیرتجاری است یا مجوز تجاری از نویسندگانش دارید. پیش از پذیرش، مجوز را در صفحهٔ پروژه بخوانید. پذیرش با مبنا و یادداشت شما ثبت می‌شود و هر زمان می‌توانید آن را لغو کنید.",
	},
}

// Notices returns the notice of a gated entry in every shipped locale (nil for an ungated entry).
func Notices(id string) []Notice {
	var out []Notice
	for _, loc := range []string{"en", "fa"} {
		if t, ok := noticeTexts[id][loc]; ok {
			out = append(out, Notice{Locale: loc, Text: t, SHA256: NoticeSHA(t)})
		}
	}
	return out
}

// NoticeDigests lists the digests the server accepts for an entry.
func NoticeDigests(id string) []string {
	var out []string
	for _, n := range Notices(id) {
		out = append(out, n.SHA256)
	}
	return out
}
