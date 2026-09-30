package plugin

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var flynnReplaceLine = regexp.MustCompile(`(?m)^replace github.com/randy-girard/flynn\s+=>\s+\S+`)

// flynnRootForPlugin is the Flynn checkout plugin-build must compile against
// so discoverd clients send Auth-Key (SEC-003). GitHub-pinned go.mod versions
// predate that. Order: FlynnSourceRoot, then a flynn/ sibling of the plugin
// (FlynnWorkspace layout).
func flynnRootForPlugin(pluginRoot string) string {
	if r := FlynnSourceRoot(); r != "" {
		return r
	}
	pluginRoot = strings.TrimSpace(pluginRoot)
	if pluginRoot == "" {
		return ""
	}
	sib := filepath.Clean(filepath.Join(pluginRoot, "..", "flynn"))
	if isFlynnModule(sib) {
		return sib
	}
	return ""
}

// replacePluginFlynnModule points the plugin go.mod at this Flynn checkout
// so plugin APIs send DISCOVERD_AUTH_KEY (SEC-003). GitHub-pinned plugins
// otherwise compile the pre-auth discoverd client. Restore after plugin-build
// so the checkout is not left dirty.
func replacePluginFlynnModule(pluginRoot, flynnRoot string) (func(), error) {
	pluginRoot = strings.TrimSpace(pluginRoot)
	flynnRoot = strings.TrimSpace(flynnRoot)
	if pluginRoot == "" || flynnRoot == "" || !isFlynnModule(flynnRoot) {
		return nil, nil
	}
	goMod := filepath.Join(pluginRoot, "go.mod")
	orig, err := os.ReadFile(goMod)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	updated := applyFlynnReplaceLine(string(orig), flynnRoot)
	if updated == string(orig) {
		return nil, nil
	}
	sumPath := filepath.Join(pluginRoot, "go.sum")
	sumOrig, sumErr := os.ReadFile(sumPath)
	if err := os.WriteFile(goMod, []byte(updated), 0644); err != nil {
		return nil, err
	}
	return func() {
		_ = os.WriteFile(goMod, orig, 0644)
		if sumErr == nil {
			_ = os.WriteFile(sumPath, sumOrig, 0644)
		}
	}, nil
}

func applyFlynnReplaceLine(goMod, flynnRoot string) string {
	line := "replace github.com/randy-girard/flynn => " + filepath.ToSlash(flynnRoot)
	if flynnReplaceLine.MatchString(goMod) {
		return flynnReplaceLine.ReplaceAllString(goMod, line)
	}
	if !strings.HasSuffix(goMod, "\n") {
		goMod += "\n"
	}
	return goMod + "\n" + line + "\n"
}
