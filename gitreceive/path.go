package main

import (
	"strings"

	"github.com/randy-girard/flynn/controller/utils"
)

const previewPathPrefix = "_preview/"

func parseGitRepoPath(path, suffix string) (kind, name string) {
	name = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSuffix(path, suffix), "/"), ".git")
	if strings.HasPrefix(name, previewPathPrefix) {
		pipe := strings.Trim(strings.TrimPrefix(name, previewPathPrefix), "/")
		if pipe != "" && !strings.Contains(pipe, "/") && utils.AppNamePattern.MatchString(pipe) {
			return "preview", pipe
		}
		return "", ""
	}
	return "app", name
}
