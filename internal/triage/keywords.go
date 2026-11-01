package triage

import "regexp"

// Risk areas (policy keys risk.area.<area>).
var Areas = []string{"auth", "payment", "migration", "data_deletion", "ci_config", "cross_service"}

// areaPatterns detect each area in English and Persian. Latin words use word boundaries; Persian is matched as text.
var areaPatterns = map[string]*regexp.Regexp{
	"auth":          regexp.MustCompile(`(?i)\b(?:authn|authz|authenticat\w*|authoriz\w*|log ?in|log ?out|sign ?in|password\w*|oauth\w*|sso|jwt|session\w*|credential\w*|permission\w*|2fa|mfa|rbac)\b|احراز هویت|رمز عبور|کلمه عبور|ورود به|دسترسی کاربر|مجوز دسترسی|نشست کاربر`),
	"payment":       regexp.MustCompile(`(?i)\b(?:payments?|checkout|credit card|refunds?|charges?|stripe|paypal|payout|subscription)\b|پرداخت|درگاه|تراکنش|بازپرداخت`),
	"migration":     regexp.MustCompile(`(?i)\b(?:migrations?|migrate\w*|schema change|alter table|drop column|backfill\w*|data migration)\b|مایگریشن|مهاجرت داده|تغییر ساختار پایگاه`),
	"data_deletion": regexp.MustCompile(`(?i)\b(?:data deletion|delete all|purge\w*|drop table|truncate|gdpr|erase\w*|wipe\w*|hard delete)\b|حذف داده|پاکسازی داده|حذف دائمی`),
	"ci_config":     regexp.MustCompile(`(?i)(?:\.gitlab-ci|\.github/workflows|\bci/cd\b|\bci pipeline\b|github actions|\bdockerfile\b|deployment config\w*|\bhelm chart\w*)|پیکربندی ci|پایپ‌?لاین ci`),
	"cross_service": regexp.MustCompile(`(?i)\b(?:cross-?service|microservices?|service boundar\w+|api contract|multiple services|other services?|several services)\b|چند سرویس|بین سرویس`),
}

// DetectAreas returns the risk areas the texts mention, in the fixed Areas order.
func DetectAreas(texts ...string) []string {
	var out []string
	for _, area := range Areas {
		re := areaPatterns[area]
		for _, t := range texts {
			if re.MatchString(t) {
				out = append(out, area)
				break
			}
		}
	}
	return out
}
