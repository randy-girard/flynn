package cli

import (
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/randy-girard/flynn/host/config"
)

// redactedMarker replaces every secret removed from collect-debug-info output.
const redactedMarker = "[REDACTED]"

// minRedactValueLen keeps short values such as "true" or "1" from being
// replaced everywhere they appear.
const minRedactValueLen = 6

// debugRedactor removes secrets from collect-debug-info output. It replaces
// known secret values (host.json env, the process env and job env) and
// anything that looks like a secret: KEY=value pairs with secret-like names,
// passwords in URLs, Authorization headers, and password flags on command
// lines.
type debugRedactor struct {
	values map[string]struct{}
}

func newDebugRedactor() *debugRedactor {
	return &debugRedactor{values: make(map[string]struct{})}
}

// AddValue registers a literal secret value.
func (r *debugRedactor) AddValue(v string) {
	v = strings.TrimSpace(v)
	if len(v) < minRedactValueLen {
		return
	}
	r.values[v] = struct{}{}
	if p := urlPassword(v); p != "" && len(p) >= minRedactValueLen {
		r.values[p] = struct{}{}
	}
}

// AddEnv registers the values of secret-looking variables in env, and the
// password of any URL-valued variable (DATABASE_URL and friends).
func (r *debugRedactor) AddEnv(env map[string]string) {
	for k, v := range env {
		if isSecretEnvName(k) {
			r.AddValue(v)
		} else if p := urlPassword(v); p != "" {
			r.AddValue(p)
		}
	}
}

// AddEnviron registers secrets from a KEY=value list such as os.Environ().
func (r *debugRedactor) AddEnviron(environ []string) {
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	r.AddEnv(env)
}

// AddHostConfig registers secrets from the env map of a flynn-host config
// file (/etc/flynn/host.json). A missing or unreadable file is not an error:
// collect-debug-info may run without root.
func (r *debugRedactor) AddHostConfig(path string) error {
	c, err := config.Open(path)
	if err != nil {
		if os.IsNotExist(err) || os.IsPermission(err) {
			return nil
		}
		return err
	}
	r.AddEnv(c.Env)
	return nil
}

func (r *debugRedactor) isKnownValue(v string) bool {
	_, ok := r.values[v]
	return ok
}

var secretNameParts = []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "CREDENTIAL", "PRIVATE"}

// isSecretEnvName reports whether an env var name looks like it holds a
// secret (CONTROLLER_KEY, DISCOVERD_AUTH_KEY, PGPASSWORD, GITHUB_TOKEN, ...).
func isSecretEnvName(name string) bool {
	n := strings.ToUpper(name)
	if n == "KEY" || strings.HasSuffix(n, "_KEY") || strings.HasSuffix(n, "KEY_BASE") || strings.Contains(n, "_KEY_") {
		return true
	}
	for _, p := range secretNameParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// secretEnvNames lists the keys of env whose values printJobDesc must hide.
func secretEnvNames(env map[string]string) []string {
	var names []string
	for k, v := range env {
		if isSecretEnvName(k) || urlPassword(v) != "" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	return names
}

func urlPassword(v string) string {
	if !strings.Contains(v, "://") {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.User == nil {
		return ""
	}
	p, _ := u.User.Password()
	return p
}

var redactPatterns = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Authorization: Bearer xxx, Authorization: token xxx, "authorization":"xxx"
	{regexp.MustCompile(`(?i)(authorization"?\s*[:=]\s*"?(?:(?:bearer|basic|token|digest)\s+)?)([^\s"',]+)`), "${1}" + redactedMarker},
	// scheme://user:password@host
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^/\s:@'"]*):([^/\s@'"]+)@`), "${1}:" + redactedMarker + "@"},
	// https://token@host
	{regexp.MustCompile(`(?i)(https?://)([^/\s:@'"]+)@`), "${1}" + redactedMarker + "@"},
	// --admin-password=x, --github-token x, --secret-key=x
	{regexp.MustCompile(`(?i)(\s--?[a-z0-9-]*(?:password|passwd|secret|token|api-key|auth-key|access-key)[a-z0-9-]*)(=|\s+)([^\s"']+)`), "${1}${2}" + redactedMarker},
	// NAME=value / "NAME": "value" with a secret-like name
	{regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:password|passwd|secret|token|credential|private_key|_key)[a-z0-9_.-]*"?\s*[=:]\s*"?)([^\s"',;&]+)`), "${1}" + redactedMarker},
	// redis-cli -a password
	{regexp.MustCompile(`(\bredis-cli\b[^\n]*?\s-a\s+)(\S+)`), "${1}" + redactedMarker},
	// mysql -ppassword
	{regexp.MustCompile(`(\b(?:mysql\w*|mariadb\w*|mariabackup)\b[^\n]*?\s-p)(\S+)`), "${1}" + redactedMarker},
	// curl -u user:password, --user user:password
	{regexp.MustCompile(`(\s(?:-u|--user)(?:\s+|=))([^\s:]*:)(\S+)`), "${1}${2}" + redactedMarker},
}

// Redact returns s with every known secret value and secret-looking token
// replaced by [REDACTED].
func (r *debugRedactor) Redact(s string) string {
	values := make([]string, 0, len(r.values))
	for v := range r.values {
		values = append(values, v)
	}
	// Longest first so a value that contains another is removed whole.
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		s = strings.ReplaceAll(s, v, redactedMarker)
	}
	for _, p := range redactPatterns {
		s = p.re.ReplaceAllString(s, p.repl)
	}
	return s
}
