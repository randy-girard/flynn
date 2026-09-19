package plugin

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/randy-girard/flynn/pkg/version"
)

// pluginCalVerRE matches Flynn plugin tags: vYYYYMMDD.N.B, and the older
// two-part vYYYYMMDD.N (treated as B=0).
var pluginCalVerRE = regexp.MustCompile(`^v([0-9]{8})\.([0-9]+)(?:\.([0-9]+))?$`)

// PluginCalVer is a plugin GitHub release tag. P is the plugin-only increment
// so a plugin can ship without a new Flynn version (same YYYYMMDD.N).
// Flynn itself is always two-part vYYYYMMDD.N (P unused / zero).
type PluginCalVer struct {
	Date int // YYYYMMDD
	N    int
	P    int
}

// ParsePluginCalVer reads vYYYYMMDD.N or vYYYYMMDD.N.B. A missing patch is 0.
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

// ParseFlynnCalVer reads a Flynn release vYYYYMMDD.N (two-part only).
// A commit suffix (-abc123) is stripped the same way as version.Release.
func ParseFlynnCalVer(s string) (PluginCalVer, bool) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "-"); i >= 0 {
		s = s[:i]
	}
	v, ok := ParsePluginCalVer(s)
	if !ok {
		return PluginCalVer{}, false
	}
	// Flynn releases are always two-part. A three-part tag is a plugin.
	if strings.Count(s, ".") != 1 {
		return PluginCalVer{}, false
	}
	return v, true
}

// FlynnLine is the Flynn release this plugin tag requires: vYYYYMMDD.N.
func (v PluginCalVer) FlynnLine() string {
	return fmt.Sprintf("v%d.%d", v.Date, v.N)
}

func (v PluginCalVer) String() string {
	if v.P == 0 {
		return v.FlynnLine()
	}
	return fmt.Sprintf("v%d.%d.%d", v.Date, v.N, v.P)
}

// ComparePluginCalVer returns -1, 0, or 1 as a compared to b. Two-part tags
// compare equal to the same date.N with P=0. Parsed calver tags sort above
// non-calver strings so plugin:update --ref omitted prefers vYYYYMMDD.N.B.
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

// PluginMatchesFlynn is true when pluginTag is vYYYYMMDD.N or vYYYYMMDD.N.B
// for the same date.N as flynnVersion (vYYYYMMDD.N). Non-calver strings never match.
func PluginMatchesFlynn(pluginTag, flynnVersion string) bool {
	p, ok := ParsePluginCalVer(pluginTag)
	if !ok {
		return false
	}
	f, ok := ParseFlynnCalVer(flynnVersion)
	if !ok {
		return false
	}
	return p.Date == f.Date && p.N == f.N
}

// HighestCompatiblePluginTag returns the highest vYYYYMMDD.N.B whose date.N
// matches flynnVersion. It never returns a newer Flynn date.N.
func HighestCompatiblePluginTag(tags []string, flynnVersion string) (string, bool) {
	f, ok := ParseFlynnCalVer(flynnVersion)
	if !ok {
		return "", false
	}
	best := ""
	for _, tag := range tags {
		if !PluginMatchesFlynn(tag, f.FlynnLine()) {
			continue
		}
		if best == "" || ComparePluginCalVer(tag, best) > 0 {
			best = tag
		}
	}
	return best, best != ""
}

// ClusterFlynnVersion is the running Flynn release (commit suffix stripped).
func ClusterFlynnVersion() string {
	return version.Release()
}

func incompatiblePluginError(pluginTag string, flynn PluginCalVer) error {
	want := flynn.FlynnLine()
	if p, ok := ParsePluginCalVer(pluginTag); ok {
		return fmt.Errorf("plugin release %s requires Flynn %s images; this cluster is running %s", pluginTag, p.FlynnLine(), want)
	}
	return fmt.Errorf("plugin release %s does not match Flynn %s (need vYYYYMMDD.N or vYYYYMMDD.N.B for this Flynn)", pluginTag, want)
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
