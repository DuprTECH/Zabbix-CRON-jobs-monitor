package crontab

import "regexp"

// The command line of a cron job is sent to Zabbix ({#CMD}) and ends up in
// item descriptions, problems and e-mails. Secrets that are commonly put
// directly into crontabs are replaced by "***" before that.
var secretPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// API_KEY=..., DB_PASSWORD="...", ZBX_TOKEN=...
	{regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:key|token|pass|passwd|password|pwd|secret|auth|credentials?)[A-Za-z0-9_]*)=("[^"]*"|'[^']*'|\S+)`), "$1=***"},
	// --password=..., --api-key ..., --token ...
	{regexp.MustCompile(`(?i)(\s--?[A-Za-z0-9_-]*(?:password|passwd|pass|token|secret|apikey|api-key|api_key|auth|key)[A-Za-z0-9_-]*)(=|\s+)("[^"]*"|'[^']*'|[^\s-]\S*)`), "$1$2***"},
	// mysql/mysqldump -pSECRET
	{regexp.MustCompile(`(\s-p)([^\s-]\S*)`), "$1***"},
	// https://user:password@host
	{regexp.MustCompile(`(://[^:/\s@]+:)[^@\s]+@`), "$1***@"},
}

// MaskSecrets hides likely secrets in a command line.
func MaskSecrets(s string) string {
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
