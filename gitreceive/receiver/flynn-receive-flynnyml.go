package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/flynn/go-docopt"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/appmanifest"
	"github.com/randy-girard/flynn/pkg/flynnyml"
	"github.com/randy-girard/flynn/pkg/random"
)

var (
	cachedFlynnTree    *flynnyml.Tree
	cachedFlynnTreeErr error
	cachedFlynnTreeSet bool
)

func resetFlynnTree() {
	cachedFlynnTree = nil
	cachedFlynnTreeErr = nil
	cachedFlynnTreeSet = false
}

func flynnTree() (*flynnyml.Tree, error) {
	if cachedFlynnTreeSet {
		return cachedFlynnTree, cachedFlynnTreeErr
	}
	tree, err := readFlynnTree()
	cachedFlynnTree = tree
	cachedFlynnTreeErr = err
	cachedFlynnTreeSet = true
	return tree, err
}

func readFlynnTree() (*flynnyml.Tree, error) {
	rs, ok := buildInput.(io.ReadSeeker)
	if !ok || buildInput == nil {
		return nil, nil
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	tree, err := flynnyml.ReadTree(rs)
	if _, seekErr := rs.Seek(0, io.SeekStart); seekErr != nil && err == nil {
		return nil, seekErr
	}
	return tree, err
}

func flynnYmlPresent(tree *flynnyml.Tree) bool {
	return tree != nil && len(bytes.TrimSpace(tree.FlynnYml)) > 0
}

func rejectFlynnYml(stack string, present bool) error {
	if present && stack != stackContainer {
		return errors.New(flynnyml.StackRequired)
	}
	return nil
}

// applyFlynnYmlBuildEnv is the dockerbuilder input for flynn.yml. The spec
// carries build-arg values, so it must not be copied from the release env
// and must not be printed.
func applyFlynnYmlBuildEnv(jobEnv map[string]string, spec string) {
	if jobEnv == nil {
		return
	}
	jobEnv["FLYNN_BUILD_SPEC"] = spec
	for _, k := range []string{"DOCKERFILE", "IMAGE_ARTIFACT_ID", "DATABASE_URL", "CONTROLLER_KEY"} {
		delete(jobEnv, k)
	}
}

func deployContainerManifest(client controller.Client, app *ct.App, prevRelease *ct.Release, args *docopt.Args, releaseEnv map[string]string, meta map[string]string, pushEnv map[string]string, tree *flynnyml.Tree) error {
	dockerbuilderImageID := os.Getenv("DOCKERBUILDER_24_IMAGE_ID")
	if dockerbuilderImageID == "" {
		return fmt.Errorf("DOCKERBUILDER_24_IMAGE_ID not configured on gitreceive")
	}
	dockerBuilder, err := client.GetArtifact(dockerbuilderImageID)
	if err != nil {
		return fmt.Errorf("Error getting dockerbuilder image: %s", err)
	}
	manifest, err := flynnyml.Parse(tree.FlynnYml)
	if err != nil {
		return err
	}
	for _, img := range manifest.Build.Images {
		if err := tree.CheckPath(img.Dockerfile); err != nil {
			return err
		}
	}
	if err := manifest.Resolve(); err != nil {
		return err
	}
	if manifest.HasSetup() {
		if tree.HasAppJSON {
			receivePrintln(flynnyml.SetupIgnoredLine)
		} else {
			prevRelease, releaseEnv, err = applyFlynnYmlSetup(client, app, prevRelease, releaseEnv, pushEnv, manifest)
			if err != nil {
				return err
			}
		}
	}

	plans, err := manifest.PlannedBuilds()
	if err != nil {
		return err
	}
	ids := make(map[string]string, len(plans))
	for _, plan := range plans {
		ids[plan.Key()] = random.UUID()
	}
	spec, err := flynnyml.EncodeBuildSpec(plans, ids, manifest.Build.Config)
	if err != nil {
		return err
	}
	receivePrintf("-----> Building %s from flynn.yml...\n", app.Name)
	printQemuTranslationNotice(os.Stdout)
	jobEnv := dockerBuildJobEnv(os.Getenv("CONTROLLER_KEY"), "", args.String["<rev>"], releaseEnv, app.ID)
	applyFlynnYmlBuildEnv(jobEnv, spec)

	resources := containerBuildResources(prevRelease)
	gitOut := newGitPushFilter(syncStdout(os.Stdout))
	placeholderID, err := runScheduledBuild(client, app, prevRelease, dockerBuilder, jobEnv, "dockerbuilder", []string{"/builder/build.sh"}, resources, releaseEnv, gitOut, gitOut)
	if err != nil {
		gitOut.flush()
		return fmt.Errorf("Build failed: %s", err)
	}
	gitOut.flush()

	byName := make(map[string]*ct.Artifact, len(manifest.Build.Images))
	for _, plan := range plans {
		artifact, err := client.GetArtifact(ids[plan.Key()])
		if err != nil {
			return fmt.Errorf("Error getting image artifact: %s", err)
		}
		for _, name := range plan.Names {
			byName[name] = artifact
		}
	}
	receivePrintf("-----> Creating release...\n")
	release, err := flynnyml.BuildRelease(app.Name, prevRelease, manifest, byName, releaseEnv, containerReleaseMeta(prevRelease, meta), containerExtraProcesses(prevRelease))
	if err != nil {
		return err
	}
	return finishDeploy(client, app, prevRelease, release, release.Processes, placeholderID)
}

func applyFlynnYmlSetup(client controller.Client, app *ct.App, prevRelease *ct.Release, releaseEnv, pushEnv map[string]string, manifest *flynnyml.Manifest) (*ct.Release, map[string]string, error) {
	ap, err := appmanifest.New(client, receiveWriter(), true)
	if err != nil {
		return nil, nil, err
	}
	known := func(name string) bool {
		return ap.Catalog != nil && ap.Catalog.AddonByType(name) != nil
	}
	addons := make([]appmanifest.ContainerAddon, 0, len(manifest.Setup.Addons))
	types := make([]string, 0, len(manifest.Setup.Addons))
	as := make([]string, 0, len(manifest.Setup.Addons))
	for _, addon := range manifest.Setup.Addons {
		typ, err := flynnyml.ResolveAddonType(addon.Plan, addon.Type, known)
		if err != nil {
			return nil, nil, err
		}
		addons = append(addons, appmanifest.ContainerAddon{Type: typ, As: addon.As, Runtime: addon.Runtime})
		types = append(types, typ)
		as = append(as, addon.As)
	}
	if err := flynnyml.ConfigCollides(manifest.Setup.Config, types, as); err != nil {
		return nil, nil, err
	}
	if _, err := ap.ApplyContainerSetup(app.ID, addons, manifest.Setup.Config); err != nil {
		return nil, nil, err
	}
	head, err := controller.HeadRelease(client, app.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("Error getting current app release: %s", err)
	}
	if head == nil {
		head = prevRelease
	}
	next := map[string]string{}
	if head != nil && head.Env != nil {
		for k, v := range head.Env {
			next[k] = v
		}
	}
	for k, v := range pushEnv {
		next[k] = v
	}
	scrubReleaseEnv(next)
	return head, next, nil
}

func containerBuildResources(prev *ct.Release) resource.Resources {
	resources := resource.Defaults()
	if limit, err := resource.ParseLimit(resource.TypeTempDisk, "4G"); err == nil {
		resources[resource.TypeTempDisk] = resource.Spec{Limit: &limit, Request: &limit}
	}
	if prev != nil {
		if db, ok := prev.Processes["dockerbuilder"]; ok && db.Resources != nil {
			return db.Resources
		}
	}
	if rawLimit := os.Getenv("DOCKERBUILDER_DEFAULT_MEMORY_LIMIT"); rawLimit != "" {
		if limit, err := resource.ParseLimit(resource.TypeMemory, rawLimit); err == nil {
			resources[resource.TypeMemory] = resource.Spec{Limit: &limit, Request: &limit}
		}
	}
	return resources
}

func containerReleaseMeta(prev *ct.Release, meta map[string]string) map[string]string {
	out := map[string]string{}
	if prev != nil && prev.Meta != nil {
		for k, v := range prev.Meta {
			out[k] = v
		}
	}
	for k, v := range meta {
		out[k] = v
	}
	out["git"] = "true"
	out["slugrunner.stack"] = stackContainer
	return out
}

func containerExtraProcesses(prev *ct.Release) map[string]ct.ProcessType {
	extra := map[string]ct.ProcessType{}
	if prev != nil {
		if db, ok := prev.Processes["dockerbuilder"]; ok {
			extra["dockerbuilder"] = db
		}
	}
	return extra
}
