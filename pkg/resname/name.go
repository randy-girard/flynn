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

// MergeAttachment copies a provision response onto an app.
// The resource always gets PREFIX_WORD_DATABASE_URL (pg-harbor-kxmnpq →
// PG_HARBOR_DATABASE_URL). --as NAME adds NAME_URL as well. When the app does
// not already have the engine's usual variable (DATABASE_URL, REDIS_URL,
// KAFKA_URL, CLICKHOUSE_URL), that variable is set to the same URL. Existing
// values are left alone. Every *_URL returned here is an attachment and must
// not be changed with env:set.
func MergeAttachment(existing, incoming map[string]string, as string) map[string]string {
	out := map[string]string{}
	if len(incoming) == 0 {
		return out
	}
	name, conv := resourceIdentity(incoming)
	url := connectionURL(incoming, conv)
	if url == "" || name == "" {
		for k, v := range incoming {
			out[k] = v
		}
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
	if strings.TrimSpace(incoming["FLYNN_POSTGRES"]) != "" && !taken("POSTGRES_URL") {
		out["POSTGRES_URL"] = url
	}
	for k, v := range incoming {
		if strings.HasSuffix(k, "_URL") || taken(k) {
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

func resourceIdentity(env map[string]string) (name, urlKey string) {
	for _, id := range identityEnv {
		if v := strings.TrimSpace(env[id.name]); v != "" {
			return v, id.url
		}
	}
	return "", ""
}

// ExtraDatabaseURL is PREFIX_WORD_DATABASE_URL. If that key is taken, the
// six-letter suffix is included so two resources that share a word stay distinct.
func ExtraDatabaseURL(resourceApp string, taken func(string) bool) string {
	prefix, word, suffix, ok := splitAppName(resourceApp)
	if !ok {
		return ""
	}
	key := strings.ToUpper(prefix+"_"+word) + "_DATABASE_URL"
	if taken != nil && taken(key) {
		key = strings.ToUpper(prefix+"_"+word+"_"+suffix) + "_DATABASE_URL"
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
