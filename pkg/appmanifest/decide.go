package appmanifest

import (
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
)

// AppState is the controller view Decide uses. Pipeline membership is not
// part of it: an app already in a pipeline still auto-applies.
type AppState struct {
	App             *ct.App
	Head            *ct.Release
	Resources       int
	ExtraRoutes     int
	GitHubConnected bool
	Scaled          bool
	// Environment is the pipeline environment, or empty when the app is not
	// in a pipeline. GitHub connected alone does not skip a preview app.
	Environment string
}

// Decide reports whether to apply, resume, skip, or ignore the file.
// filePresent is a non-empty root flynn-app.json. Invalid JSON is only
// parsed when the action is apply or resume.
func Decide(st AppState, filePresent bool) Action {
	status := metaValue(st.App, MetaStatus)
	switch status {
	case StatusApplied:
		if filePresent {
			return ActionSkip
		}
		return ActionNone
	case StatusPending:
		return ActionResume
	}
	if !filePresent {
		return ActionNone
	}
	if configured(st) {
		return ActionSkip
	}
	return ActionApply
}

func configured(st AppState) bool {
	if st.Resources > 0 || st.ExtraRoutes > 0 || st.Scaled {
		return true
	}
	if st.GitHubConnected && !strings.EqualFold(st.Environment, "preview") {
		return true
	}
	if releaseBlocks(st.Head) {
		return true
	}
	app := st.App
	if app == nil {
		return false
	}
	if s := strings.TrimSpace(app.Strategy); s != "" && s != "all-at-once" {
		return true
	}
	if app.DeployTimeout != 0 && app.DeployTimeout != ct.DefaultDeployTimeout {
		return true
	}
	if metaValue(app, MetaProtected) == "true" {
		return true
	}
	for k := range app.Meta {
		if !ManagedMeta(k) {
			return true
		}
	}
	return false
}

func releaseBlocks(rel *ct.Release) bool {
	if rel == nil || strings.TrimSpace(rel.ID) == "" {
		return false
	}
	if rel.Meta != nil && rel.Meta[MetaPreviewSeed] == "true" {
		return false
	}
	return true
}

// ManagedMeta is set by the controller, gitreceive, or the pipeline plugin
// and does not by itself make an app configured.
func ManagedMeta(key string) bool {
	switch key {
	case ct.MetaGCMaxInactiveSlugReleases,
		ct.MetaAppBuilding,
		"flynn-controller.build-failed",
		"flynn-system-app",
		"flynn-pipeline",
		"flynn-preview-remote",
		MetaPreviewRoute,
		MetaPreviewBranch,
		MetaPreviewSHA,
		"flynn-pipeline.preview-pr":
		return true
	}
	return key == MetaStatus || strings.HasPrefix(key, MetaStatus+".")
}

func metaValue(app *ct.App, key string) string {
	if app == nil || app.Meta == nil {
		return ""
	}
	return strings.TrimSpace(app.Meta[key])
}

// Protected reports the manifest flag stored on the app.
func Protected(app *ct.App) bool {
	return metaValue(app, MetaProtected) == "true"
}
