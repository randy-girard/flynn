// Package resname builds database resource app names.
//
// MySQL and the other engines use <prefix>-<word>-<6 letters>, for example
// mysql-harbor-kxmnpq. Postgres and Redis instances use <engine>-<word>-<5
// digits>, for example postgresql-concave-48291 and redis-harbor-48291.
// Callers retry when the name is already taken so two resources never share it.
package resname

import (
	"crypto/rand"
	"sort"
	"strings"
)

// words are short readable names. The six-letter suffix is what makes the
// full name unique.
var words = []string{
	"alder", "alpine", "amber", "arroyo", "aspen", "atoll", "basin", "bayou",
	"bluff", "boulder", "brook", "canyon", "cape", "cay", "cedar", "chaparral",
	"cliff", "comet", "concave", "cove", "creek", "delta", "dune", "ember",
	"estuary", "fen", "fjord", "glacier", "glen", "gorge", "granite", "grove",
	"harbor", "heath", "highland", "horizon", "inlet", "island", "juniper",
	"kelp", "knoll", "lagoon", "lake", "ledge", "marsh", "meadow", "mesa",
	"mist", "moraine", "north", "orchid", "oxbow", "peak", "pebble", "pine",
	"plateau", "pond", "prairie", "quartz", "range", "reef", "ridge", "river",
	"rock", "savanna", "shore", "sierra", "spruce", "summit", "swamp", "tide",
	"timber", "tundra", "upland", "valley", "vista", "willow", "woodland",
	"yarrow",
}

const alphabet = "abcdefghijklmnopqrstuvwxyz"

const digitAlphabet = "0123456789"

const postgresAttachPrefix = "FLYNN_POSTGRESQL_"

const redisAttachPrefix = "FLYNN_REDIS_"

var attachmentColors = []string{
	"ALABASTER", "AMBER", "APRICOT", "AQUA", "AZURE", "BEIGE", "BLACK", "BLUE",
	"BONE", "BRASS", "BRONZE", "BROWN", "BURGUNDY", "CARMINE", "CELADON",
	"CERULEAN", "CHARTREUSE", "CHESTNUT", "CINNAMON", "CITRINE", "COBALT",
	"COPPER", "CORAL", "CREAM", "CRIMSON", "CYAN", "DENIM", "EBONY", "EMERALD",
	"FLAX", "FOREST", "FUCHSIA", "GARNET", "GINGER", "GOLD", "GRAPHITE", "GRAY",
	"GREEN", "HAZEL", "HONEY", "ICE", "INDIGO", "IVORY", "JADE", "KHAKI",
	"LAVENDER", "LEMON", "LILAC", "LIME", "MAGENTA", "MAHOGANY", "MAROON",
	"MAUVE", "MINT", "MOSS", "MUSTARD", "NAVY", "OCHRE", "OLIVE", "ONYX",
	"OPAL", "ORANGE", "PEACH", "PEARL", "PERIWINKLE", "PINK", "PISTACHIO",
	"PLATINUM", "PLUM", "PURPLE", "RED", "ROSE", "RUBY", "RUST", "SAFFRON",
	"SAGE", "SALMON", "SAND", "SCARLET", "SEPIA", "SIENNA", "SILVER", "SLATE",
	"STEEL", "TAN", "TAUPE", "TEAL", "TOMATO", "TURQUOISE", "UMBER",
	"VERMILION", "VIOLET", "WALNUT", "WHEAT", "WHITE", "WINE", "YELLOW",
}

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

// IsolatedService is a provisioned datastore app (postgresql-concave-48291,
// redis-harbor-48291, or the older pg-harbor-kxmnpq / redis-harbor-abcdef
// forms). User jobs resolve leader.<name>.discoverd for these; other
// discoverd names stay internal.
func IsolatedService(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if postgresInstanceName(name) || redisInstanceName(name) {
		return true
	}
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
	if prefix == "pg" && strings.HasPrefix(name, "postgresql-") {
		return name
	}
	return prefix + "-" + name
}

func postgresInstanceName(name string) bool {
	const p = "postgresql-"
	if !strings.HasPrefix(name, p) {
		return false
	}
	rest := name[len(p):]
	j := strings.LastIndexByte(rest, '-')
	if j <= 0 || j == len(rest)-1 {
		return false
	}
	suffix := rest[j+1:]
	if n := len(suffix); n < 5 || n > 8 {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	owner := rest[:j]
	if owner == "" {
		return false
	}
	for _, c := range owner {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return owner[0] != '-' && owner[len(owner)-1] != '-'
}

func redisInstanceName(name string) bool {
	const p = "redis-"
	if !strings.HasPrefix(name, p) {
		return false
	}
	rest := name[len(p):]
	j := strings.LastIndexByte(rest, '-')
	if j <= 0 || j == len(rest)-1 {
		return false
	}
	suffix := rest[j+1:]
	if n := len(suffix); n < 5 || n > 8 {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	owner := rest[:j]
	if owner == "" {
		return false
	}
	for _, c := range owner {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return owner[0] != '-' && owner[len(owner)-1] != '-'
}

func letters(n int) string {
	return fromAlphabet(alphabet, n)
}

func digits(n int) string {
	return fromAlphabet(digitAlphabet, n)
}

func fromAlphabet(set string, n int) string {
	buf := make([]byte, n)
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = set[int(raw[i])%len(set)]
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
// Every postgres provision and attach sets FLYNN_POSTGRESQL_<COLOR>_URL unless
// --as names the attachment (NAME_URL, or FLYNN_POSTGRESQL_<COLOR>_URL when
// --as is a color). A new provision also sets DATABASE_URL when that key is
// free. Redis is the same with FLYNN_REDIS_<COLOR>_URL and REDIS_URL.
// Attaching an existing resource never sets the conventional URL. Incoming
// color URLs are reused so the resource record and app release share one color
// key. Other engines get PREFIX_WORD_SUFFIX_DATABASE_URL and the engine's
// usual URL when unused, plus --as NAME_URL. Host, user, password, and
// FLYNN_* identity keys stay on the resource record, not the app.
func MergeAttachment(existing, incoming map[string]string, as string, newProvision bool) map[string]string {
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
	postgres := strings.TrimSpace(incoming["FLYNN_POSTGRES"]) != ""
	redis := strings.TrimSpace(incoming["FLYNN_REDIS"]) != ""
	switch {
	case postgres:
		if strings.TrimSpace(as) != "" {
			if key := PostgresAttachmentURLKey(as, taken); key != "" {
				out[key] = url
			}
		} else if !postgresColorAlreadySet(existing, url) {
			key := reuseIncomingPostgresColor(incoming, taken)
			if key == "" {
				key = ColorDatabaseURL(taken)
			}
			out[key] = url
		}
		if newProvision && !taken("DATABASE_URL") {
			out["DATABASE_URL"] = url
		}
	case redis:
		if strings.TrimSpace(as) != "" {
			if key := RedisAttachmentURLKey(as, taken); key != "" {
				out[key] = url
			}
		} else if !redisColorAlreadySet(existing, url) {
			key := reuseIncomingRedisColor(incoming, taken)
			if key == "" {
				key = ColorRedisURL(taken)
			}
			out[key] = url
		}
		if newProvision && !taken("REDIS_URL") {
			out["REDIS_URL"] = url
		}
	default:
		if key := ExtraDatabaseURL(name, taken); key != "" {
			out[key] = url
		}
		if key := AsURLKey(as); key != "" && !taken(key) {
			out[key] = url
		}
		if conv != "" && !taken(conv) {
			out[conv] = url
		}
	}
	return out
}

func postgresColorAlreadySet(existing map[string]string, url string) bool {
	for k, v := range existing {
		if v == url && PostgresColorURLKey(k) {
			return true
		}
	}
	return false
}

func redisColorAlreadySet(existing map[string]string, url string) bool {
	for k, v := range existing {
		if v == url && RedisColorURLKey(k) {
			return true
		}
	}
	return false
}

func reuseIncomingPostgresColor(incoming map[string]string, taken func(string) bool) string {
	var keys []string
	for k := range incoming {
		if PostgresColorURLKey(k) && (taken == nil || !taken(k)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func reuseIncomingRedisColor(incoming map[string]string, taken func(string) bool) string {
	var keys []string
	for k := range incoming {
		if RedisColorURLKey(k) && (taken == nil || !taken(k)) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
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
// Any *_URL whose value still matches a resource connection string is locked
// (FLYNN_POSTGRESQL_AMBER_URL, ANALYTICS_URL, …).
func LockedKeys(release map[string]string, resourceEnvs ...map[string]string) map[string]string {
	locked := map[string]string{}
	if release == nil {
		return locked
	}
	urls := map[string]bool{}
	same := map[string]string{}
	for _, re := range resourceEnvs {
		if re == nil {
			continue
		}
		if u := connectionURL(re, ""); u != "" {
			urls[u] = true
		}
		for k, v := range re {
			if !strings.HasSuffix(k, "_URL") || v == "" {
				continue
			}
			urls[v] = true
			same[k] = v
		}
	}
	for k, v := range release {
		if !strings.HasSuffix(k, "_URL") || v == "" {
			continue
		}
		if urls[v] || same[k] == v {
			locked[k] = v
		}
	}
	return locked
}

// ResourceEnv is the env stored on the controller resource record.
// MergeAttachment is what the app release gets (the attachment color or --as
// URL, plus DATABASE_URL / REDIS_URL on a new provision when that key is
// free). The resource itself keeps instance identity (FLYNN_POSTGRES /
// FLYNN_REDIS, role) so the dashboard and plugin CLI can find it. Extra
// incoming *_URL keys that the app did not receive are dropped so the
// resource page and env page show the same attachment. Split PGHOST/PGUSER
// keys stay off the resource.
func ResourceEnv(existing, incoming map[string]string, as string) map[string]string {
	merged := MergeAttachment(existing, incoming, as, true)
	if len(incoming) == 0 {
		return merged
	}
	out := map[string]string{}
	for k, v := range merged {
		out[k] = v
	}
	for k, v := range incoming {
		if strings.TrimSpace(v) == "" || conventionalURL(k) || splitCredentialKey(k) || k == "POSTGRES_URL" {
			continue
		}
		if strings.HasSuffix(k, "_URL") {
			if _, ok := merged[k]; !ok {
				continue
			}
		}
		out[k] = v
	}
	return out
}

// splitCredentialKey is a libpq/POSTGRES_* piece that belongs on the isolated
// instance, not the tenant resource. Connection info stays in *_URL values.
func splitCredentialKey(k string) bool {
	switch k {
	case "PGHOST", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE", "PGSSLMODE",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB", "POSTGRES_HOST", "POSTGRES_PORT",
		"REDIS_HOST", "REDIS_PORT", "REDIS_TLS_ENABLED", "REDIS_TRUSTED_CERT",
		"REDIS_MASTER_HOST", "REDIS_MASTER_PORT", "REDIS_MASTER_PASSWORD", "REDIS_MASTER_CA":
		return true
	}
	return false
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

// AsURLKey is the env var for --as NAME: NAME_URL. A trailing _URL on NAME
// is not doubled.
func AsURLKey(as string) string {
	as = strings.ToUpper(strings.TrimSpace(as))
	as = strings.TrimSuffix(as, "_URL")
	as = strings.Trim(as, "_")
	if as == "" {
		return ""
	}
	return as + "_URL"
}

// ColorDatabaseURL is FLYNN_POSTGRESQL_<COLOR>_URL for a color that taken
// does not already report.
func ColorDatabaseURL(taken func(string) bool) string {
	if len(attachmentColors) == 0 {
		return postgresAttachPrefix + "AMBER_URL"
	}
	start := index(len(attachmentColors))
	for i := 0; i < len(attachmentColors); i++ {
		color := attachmentColors[(start+i)%len(attachmentColors)]
		key := postgresAttachPrefix + color + "_URL"
		if taken == nil || !taken(key) {
			return key
		}
	}
	return postgresAttachPrefix + attachmentColors[start] + "_X_URL"
}

// ColorRedisURL is FLYNN_REDIS_<COLOR>_URL for a color that taken does not
// already report.
func ColorRedisURL(taken func(string) bool) string {
	if len(attachmentColors) == 0 {
		return redisAttachPrefix + "AMBER_URL"
	}
	start := index(len(attachmentColors))
	for i := 0; i < len(attachmentColors); i++ {
		color := attachmentColors[(start+i)%len(attachmentColors)]
		key := redisAttachPrefix + color + "_URL"
		if taken == nil || !taken(key) {
			return key
		}
	}
	return redisAttachPrefix + attachmentColors[start] + "_X_URL"
}

// PostgresAppURLKey is the attachment env var for one postgres attach onto an
// app. With no --as, that is FLYNN_POSTGRESQL_<COLOR>_URL (reusing an incoming
// color when unused). --as NAME is NAME_URL (a color short name becomes
// FLYNN_POSTGRESQL_<COLOR>_URL). DATABASE_URL is added separately by
// MergeAttachment on a new provision when that key is free.
func PostgresAppURLKey(as string, incoming map[string]string, taken func(string) bool) string {
	as = strings.TrimSpace(as)
	if as != "" {
		return PostgresAttachmentURLKey(as, taken)
	}
	if key := reuseIncomingPostgresColor(incoming, taken); key != "" {
		return key
	}
	return ColorDatabaseURL(taken)
}

// PostgresAttachmentURLKey is the env var for one postgres --as attach. --as
// uses the same short name and appends _URL. A color (AMBER) or
// FLYNN_POSTGRESQL_AMBER becomes FLYNN_POSTGRESQL_AMBER_URL. Other names
// become NAME_URL. With no --as, a free color is chosen.
func PostgresAttachmentURLKey(as string, taken func(string) bool) string {
	as = strings.ToUpper(strings.TrimSpace(as))
	as = strings.TrimSuffix(as, "_URL")
	as = strings.Trim(as, "_")
	if as == "" {
		return ColorDatabaseURL(taken)
	}
	if strings.HasPrefix(as, postgresAttachPrefix) {
		return as + "_URL"
	}
	if isAttachmentColor(as) {
		return postgresAttachPrefix + as + "_URL"
	}
	return as + "_URL"
}

// PostgresColorURLKey is FLYNN_POSTGRESQL_<COLOR>_URL (and the _X_ fallback).
func PostgresColorURLKey(k string) bool {
	k = strings.TrimSpace(k)
	if !strings.HasPrefix(k, postgresAttachPrefix) || !strings.HasSuffix(k, "_URL") {
		return false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(k, postgresAttachPrefix), "_URL")
	if mid == "" {
		return false
	}
	for _, c := range mid {
		if c >= 'A' && c <= 'Z' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// RedisAttachmentURLKey is the env var for one redis --as attach. --as uses
// the same short name and appends _URL. A color (AMBER) or FLYNN_REDIS_AMBER
// becomes FLYNN_REDIS_AMBER_URL. Other names become NAME_URL. With no --as,
// a free color is chosen.
func RedisAttachmentURLKey(as string, taken func(string) bool) string {
	as = strings.ToUpper(strings.TrimSpace(as))
	as = strings.TrimSuffix(as, "_URL")
	as = strings.Trim(as, "_")
	if as == "" {
		return ColorRedisURL(taken)
	}
	if strings.HasPrefix(as, redisAttachPrefix) {
		return as + "_URL"
	}
	if isAttachmentColor(as) {
		return redisAttachPrefix + as + "_URL"
	}
	return as + "_URL"
}

// RedisColorURLKey is FLYNN_REDIS_<COLOR>_URL (and the _X_ fallback).
func RedisColorURLKey(k string) bool {
	k = strings.TrimSpace(k)
	if !strings.HasPrefix(k, redisAttachPrefix) || !strings.HasSuffix(k, "_URL") {
		return false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(k, redisAttachPrefix), "_URL")
	if mid == "" {
		return false
	}
	for _, c := range mid {
		if c >= 'A' && c <= 'Z' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func isAttachmentColor(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	for _, c := range attachmentColors {
		if c == name {
			return true
		}
	}
	return false
}

// PostgresInstanceName is postgresql-<word>-<5 digits>, for example
// postgresql-concave-48291. taken reports names already in use.
func PostgresInstanceName(taken func(string) bool) string {
	const head = "postgresql-"
	if len(words) == 0 {
		return head + "app-" + digits(5)
	}
	for i := 0; i < 32; i++ {
		name := head + words[index(len(words))] + "-" + digits(5)
		if taken == nil || !taken(name) {
			return name
		}
	}
	return head + words[index(len(words))] + "-" + digits(8)
}

// RedisInstanceName is redis-<word>-<5 digits>, for example redis-harbor-48291.
// taken reports names already in use.
func RedisInstanceName(taken func(string) bool) string {
	const head = "redis-"
	if len(words) == 0 {
		return head + "app-" + digits(5)
	}
	for i := 0; i < 32; i++ {
		name := head + words[index(len(words))] + "-" + digits(5)
		if taken == nil || !taken(name) {
			return name
		}
	}
	return head + words[index(len(words))] + "-" + digits(8)
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
