package plugin

import (
	"fmt"
	"strings"
)

// Interp is the substitution context for plugin CLI argv templates.
// Placeholders are expanded once; values from app/resource env are inserted
// verbatim and never re-scanned (so a password containing "${resource}" stays literal).
type Interp struct {
	App         map[string]string
	AppName     string
	Resource    string
	ResourceEnv map[string]string
}

// Interpolate expands ${resource}, ${app}, ${app.KEY}, ${app.KEY|fallback}, and
// ${resource.KEY} in a manifest template. Fallback may only contain literals
// and ${resource}. ${app} is the current flynn -a app name.
func Interpolate(tmpl string, in Interp) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(tmpl) {
		j := strings.Index(tmpl[i:], "${")
		if j < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		j += i
		b.WriteString(tmpl[i:j])
		inner, end, err := readPlaceholder(tmpl, j)
		if err != nil {
			return "", err
		}
		val, err := expandPlaceholder(inner, in)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		i = end + 1
	}
	return b.String(), nil
}

func InterpolateAll(args []string, in Interp) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, err := Interpolate(a, in)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func expandPlaceholder(inner string, in Interp) (string, error) {
	switch {
	case inner == "resource":
		return in.Resource, nil
	case inner == "app":
		return in.AppName, nil
	case strings.HasPrefix(inner, "app."):
		rest := strings.TrimPrefix(inner, "app.")
		key, fallback, hasFB := strings.Cut(rest, "|")
		if !validIdent(key) {
			return "", fmt.Errorf("invalid plugin CLI placeholder ${%s}", inner)
		}
		var val string
		if in.App != nil {
			val = in.App[key]
		}
		if val != "" {
			return val, nil
		}
		if hasFB {
			return interpolateFallback(fallback, in)
		}
		return "", nil
	case strings.HasPrefix(inner, "resource."):
		key := strings.TrimPrefix(inner, "resource.")
		if !validIdent(key) {
			return "", fmt.Errorf("invalid plugin CLI placeholder ${%s}", inner)
		}
		if in.ResourceEnv != nil {
			return in.ResourceEnv[key], nil
		}
		return "", nil
	default:
		return "", fmt.Errorf("unknown plugin CLI placeholder ${%s}", inner)
	}
}

func interpolateFallback(fb string, in Interp) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(fb) {
		j := strings.Index(fb[i:], "${")
		if j < 0 {
			b.WriteString(fb[i:])
			break
		}
		j += i
		b.WriteString(fb[i:j])
		inner, end, err := readPlaceholder(fb, j)
		if err != nil {
			return "", err
		}
		if inner != "resource" {
			return "", fmt.Errorf("fallback may only use ${resource}, got ${%s}", inner)
		}
		b.WriteString(in.Resource)
		i = end + 1
	}
	return b.String(), nil
}

// readPlaceholder returns the text inside ${...} starting at s[i] == '$',
// matching nested braces so ${app.KEY|leader.${resource}.discoverd} works.
func readPlaceholder(s string, i int) (inner string, end int, err error) {
	if i+1 >= len(s) || s[i] != '$' || s[i+1] != '{' {
		return "", 0, fmt.Errorf("invalid placeholder at %d in %q", i, s)
	}
	depth := 0
	for j := i; j < len(s); j++ {
		if j+1 < len(s) && s[j] == '$' && s[j+1] == '{' {
			depth++
			j++
			continue
		}
		if s[j] == '}' {
			depth--
			if depth == 0 {
				return s[i+2 : j], j, nil
			}
		}
	}
	return "", 0, fmt.Errorf("unclosed placeholder in %q", s)
}

func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}
