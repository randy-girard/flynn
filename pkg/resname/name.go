// Package resname builds database resource app names.
//
// A name is <prefix>-<word>-<6 letters>, for example pg-harbor-kxmnpq.
// Callers retry when the name is already taken so two resources never share it.
package resname

import (
	"crypto/rand"
	"strings"
)

// words are short readable names. The six-letter suffix is what makes the
// full name unique.
var words = []string{
	"amber", "basin", "cedar", "delta", "ember", "fjord", "grove", "harbor",
	"inlet", "juniper", "kelp", "lagoon", "meadow", "north", "orchid", "prairie",
	"quartz", "ridge", "spruce", "timber", "upland", "valley", "willow", "yarrow",
}

const alphabet = "abcdefghijklmnopqrstuvwxyz"

// Name returns prefix-word-xxxxxx. taken reports names already in use.
// A nil taken accepts the first name.
func Name(prefix string, taken func(string) bool) string {
	prefix = strings.ToLower(strings.Trim(strings.TrimSpace(prefix), "-"))
	if prefix == "" {
		prefix = "db"
	}
	for i := 0; i < 32; i++ {
		name := prefix + "-" + words[index(len(words))] + "-" + letters(6)
		if taken == nil || !taken(name) {
			return name
		}
	}
	return prefix + "-" + words[index(len(words))] + "-" + letters(8)
}

// IsolatedService is a provisioned datastore app (pg-harbor-kxmnpq). User
// jobs resolve leader.<name>.discoverd for these; other discoverd names stay
// internal.
func IsolatedService(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	i := strings.IndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return false
	}
	switch name[:i] {
	case "pg", "mysql", "redis", "mongodb", "mongo", "kafka", "clickhouse":
	default:
		return false
	}
	j := strings.LastIndexByte(name, '-')
	if j <= i {
		return false
	}
	suffix := name[j+1:]
	if n := len(suffix); n < 6 || n > 8 {
		return false
	}
	for _, c := range suffix {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	word := name[i+1 : j]
	if word == "" {
		return false
	}
	for _, c := range word {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

// Canonical turns the argument of `pg:psql <name>` into the resource app name.
// A bare word-xxxxxx is prefixed with the plugin command. A name that already
// starts with that prefix is kept, including older redis-<uuid> apps.
func Canonical(command, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	prefix := strings.ToLower(strings.TrimSpace(command))
	if name == "" || prefix == "" {
		return name
	}
	if name == prefix || strings.HasPrefix(name, prefix+"-") {
		return name
	}
	return prefix + "-" + name
}

func letters(n int) string {
	buf := make([]byte, n)
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(buf)
}

func index(n int) int {
	if n <= 1 {
		return 0
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return int(b[0]) % n
}

// identityEnv is the resource app name on a provision response, and the URL
// variable that engine uses for the first attachment on an app.
var identityEnv = []struct{ name, url string }{
	{"FLYNN_REDIS", "REDIS_URL"},
	{"FLYNN_KAFKA", "KAFKA_URL"},
	{"FLYNN_CLICKHOUSE", "CLICKHOUSE_URL"},
	{"FLYNN_MYSQL", "DATABASE_URL"},
	{"FLYNN_MONGO", "DATABASE_URL"},
	{"FLYNN_MONGODB", "DATABASE_URL"},
	{"FLYNN_POSTGRES", "DATABASE_URL"},
}

func conventionalURL(k string) bool {
	switch k {
	case "DATABASE_URL", "REDIS_URL", "KAFKA_URL", "CLICKHOUSE_URL":
		return true
	}
	return false
}

// MergeAttachment copies a provision response onto an app.
// The app gets PREFIX_WORD_SUFFIX_DATABASE_URL always (pg-harbor-kxmnpq →
// PG_HARBOR_KXMNPQ_DATABASE_URL), the engine's usual URL (DATABASE_URL,
// REDIS_URL, …) when that key is unused, and --as NAME_URL. Host, user,
// password, and FLYNN_* keys stay on the resource record, not the app.
func MergeAttachment(existing, incoming map[string]string, as string) map[string]string {
	out := map[string]string{}
	if len(incoming) == 0 {
		return out
	}
	name, conv := Identity(incoming)
	url := connectionURL(incoming, conv)
	if url == "" || name == "" {
		return out
	}
	taken := func(k string) bool {
		return strings.TrimSpace(existing[k]) != "" || out[k] != ""
	}
	if key := ExtraDatabaseURL(name, taken); key != "" {
		out[key] = url
	}
	if as = strings.ToUpper(strings.TrimSpace(as)); as != "" {
		key := as + "_URL"
		if !taken(key) {
			out[key] = url
		}
	}
	if conv != "" && !taken(conv) {
		out[conv] = url
	}
	return out
}

// EnvPrefix is the env stem for a resource app name (pg-willow-acmaos →
// PG_WILLOW_ACMAOS).
func EnvPrefix(resourceApp string) string {
	prefix, word, suffix, ok := splitAppName(resourceApp)
	if !ok {
		name := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(resourceApp), "-", "_"))
		return strings.Trim(name, "_")
	}
	return strings.ToUpper(prefix + "_" + word + "_" + suffix)
}

// UnsetAttachment is the env:unset map for removing a resource from an app.
// Keys still matching the resource record, any *_URL with the same connection
// string, and PREFIX_* keys for this resource name are cleared.
func UnsetAttachment(release, resource map[string]string) map[string]*string {
	env := map[string]*string{}
	if release == nil || resource == nil {
		return env
	}
	for k, v := range resource {
		if release[k] == v {
			env[k] = nil
		}
	}
	for k, v := range release {
		if !strings.HasSuffix(k, "_URL") || !strings.Contains(v, "://") {
			continue
		}
		for _, rv := range resource {
			if v == rv {
				env[k] = nil
				break
			}
		}
	}
	name, _ := Identity(resource)
	if prefix := EnvPrefix(name); prefix != "" {
		p := prefix + "_"
		for k := range release {
			if strings.HasPrefix(k, p) {
				env[k] = nil
			}
		}
	}
	return env
}

// LockedKeys are release env vars currently owned by a resource attachment.
// Only *_URL keys are locked (DATABASE_URL, PG_HARBOR_KXMNPQ_DATABASE_URL, …).
func LockedKeys(release map[string]string, resourceEnvs ...map[string]string) map[string]string {
	locked := map[string]string{}
	if release == nil {
		return locked
	}
	for _, re := range resourceEnvs {
		for k, v := range re {
			if !strings.HasSuffix(k, "_URL") || v == "" {
				continue
			}
			if release[k] == v {
				locked[k] = v
			}
		}
	}
	return locked
}

// ResourceEnv is the env stored on the controller resource record.
// MergeAttachment is what the app release gets (DATABASE_URL when unused, plus
// the scoped *_DATABASE_URL). The resource itself keeps instance identity
// (FLYNN_POSTGRES, POSTGRES_URL, role, host) so the dashboard and pg:psql can
// find it. Conventional app URLs are not copied onto a later resource.
func ResourceEnv(existing, incoming map[string]string, as string) map[string]string {
	merged := MergeAttachment(existing, incoming, as)
	if len(incoming) == 0 {
		return merged
	}
	out := map[string]string{}
	for k, v := range merged {
		out[k] = v
	}
	for k, v := range incoming {
		if strings.TrimSpace(v) == "" || conventionalURL(k) {
			continue
		}
		out[k] = v
	}
	return out
}

func connectionURL(incoming map[string]string, conv string) string {
	if conv != "" && strings.TrimSpace(incoming[conv]) != "" {
		return incoming[conv]
	}
	if v := strings.TrimSpace(incoming["POSTGRES_URL"]); v != "" {
		return v
	}
	for k, v := range incoming {
		if strings.HasSuffix(k, "_URL") && strings.Contains(v, "://") {
			return v
		}
	}
	return ""
}

// Identity is the resource app name and conventional URL key from a provision
// env (FLYNN_POSTGRES, FLYNN_MYSQL, FLYNN_REDIS, FLYNN_KAFKA, FLYNN_CLICKHOUSE,
// FLYNN_MONGO, FLYNN_MONGODB). The name is what operators grep, for example
// pg-harbor-kxmnpq.
func Identity(env map[string]string) (name, urlKey string) {
	if env == nil {
		return "", ""
	}
	for _, id := range identityEnv {
		if v := strings.TrimSpace(env[id.name]); v != "" {
			return v, id.url
		}
	}
	return "", ""
}

// ExtraDatabaseURL is PREFIX_WORD_SUFFIX_DATABASE_URL so the key matches the
// resource name (pg-harbor-kxmnpq → PG_HARBOR_KXMNPQ_DATABASE_URL). If that
// key is taken, an extra marker keeps the second resource distinct.
func ExtraDatabaseURL(resourceApp string, taken func(string) bool) string {
	prefix := EnvPrefix(resourceApp)
	if prefix == "" {
		return ""
	}
	key := prefix + "_DATABASE_URL"
	if taken != nil && taken(key) {
		return prefix + "_X_DATABASE_URL"
	}
	return key
}

func splitAppName(resourceApp string) (prefix, word, suffix string, ok bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(resourceApp)), "-")
	if len(parts) < 3 {
		return "", "", "", false
	}
	prefix, word, suffix = parts[0], parts[1], parts[len(parts)-1]
	if prefix == "" || word == "" || len(suffix) < 6 {
		return "", "", "", false
	}
	return prefix, word, suffix, true
}
