package flynnyml

import (
	"fmt"
	"sort"
	"strings"
)

// EncodeBuildSpec is the line protocol dockerbuilder reads. It is the build
// job input. Callers must not print it: build-arg values are in the body.
func EncodeBuildSpec(plans []PlannedBuild, artifactIDs map[string]string, args map[string]string) (string, error) {
	if len(plans) == 0 {
		return "", fmt.Errorf("flynn.yml: build.docker is required")
	}
	keys := make([]string, 0, len(args))
	for k, v := range args {
		if strings.ContainsAny(k, "\r\n") || strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("flynn.yml: build.config %s must be a single line", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", len(plans))
	for _, p := range plans {
		id := ""
		if artifactIDs != nil {
			id = artifactIDs[p.Key()]
		}
		if id == "" {
			return "", fmt.Errorf("flynn.yml: missing artifact id for %s", p.Dockerfile)
		}
		ctx := p.Context
		if ctx == "" {
			ctx = "."
		}
		target := p.Target
		if target == "" {
			target = "-"
		}
		fmt.Fprintf(&b, "%s\n%s\n%s\n%s\n%d\n", id, ctx, p.Filename, target, len(keys))
		for _, k := range keys {
			fmt.Fprintf(&b, "%s\n%s\n", k, args[k])
		}
	}
	return b.String(), nil
}
