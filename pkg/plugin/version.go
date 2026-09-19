package plugin

import (
	"regexp"
	"strconv"
	"strings"
)

// pluginCalVerRE matches Flynn plugin tags: vYYYYMMDD.N.P, and the older
// two-part vYYYYMMDD.N (treated as P=0).
var pluginCalVerRE = regexp.MustCompile(`^v([0-9]{8})\.([0-9]+)(?:\.([0-9]+))?$`)

// PluginCalVer is a plugin GitHub release tag. P is the plugin-only increment
// so a plugin can ship without a new Flynn version (same YYYYMMDD.N).
type PluginCalVer struct {
	Date int // YYYYMMDD
	N    int
	P    int
}

// ParsePluginCalVer reads vYYYYMMDD.N or vYYYYMMDD.N.P. A missing patch is 0.
func ParsePluginCalVer(s string) (PluginCalVer, bool) {
	m := pluginCalVerRE.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return PluginCalVer{}, false
	}
	date, err := strconv.Atoi(m[1])
	if err != nil {
		return PluginCalVer{}, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil {
		return PluginCalVer{}, false
	}
	p := 0
	if m[3] != "" {
		p, err = strconv.Atoi(m[3])
		if err != nil {
			return PluginCalVer{}, false
		}
	}
	return PluginCalVer{Date: date, N: n, P: p}, true
}

// ComparePluginCalVer returns -1, 0, or 1 as a compared to b. Two-part tags
// compare equal to the same date.N with P=0. Parsed calver tags sort above
// non-calver strings so plugin:update --ref omitted prefers vYYYYMMDD.N.P.
func ComparePluginCalVer(a, b string) int {
	av, aok := ParsePluginCalVer(a)
	bv, bok := ParsePluginCalVer(b)
	switch {
	case aok && bok:
		if av.Date != bv.Date {
			return cmpInt(av.Date, bv.Date)
		}
		if av.N != bv.N {
			return cmpInt(av.N, bv.N)
		}
		return cmpInt(av.P, bv.P)
	case aok:
		return 1
	case bok:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
