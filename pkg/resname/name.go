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
