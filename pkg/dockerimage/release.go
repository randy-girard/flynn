package dockerimage

import (
	"strconv"

	ct "github.com/flynn/flynn/controller/types"
	host "github.com/flynn/flynn/host/types"
)

// ReleaseOptions configures how an app release is built from an imported image.
type ReleaseOptions struct {
	Env                map[string]string
	Meta               map[string]string
	ProcessName        string
	ServiceHealthCheck bool
	ExtraProcesses     map[string]ct.ProcessType
}

// NewAppRelease builds a release that runs an imported Docker image as the app
// process. It is used by both flynn docker push and git push container deploys.
func NewAppRelease(appName string, prev *ct.Release, artifactID string, build *BuildResult, opts ReleaseOptions) *ct.Release {
	processName := opts.ProcessName
	if processName == "" {
		processName = "app"
	}

	release := &ct.Release{
		ArtifactIDs: []string{artifactID},
		Env:         copyStringMap(opts.Env),
		Meta:        copyStringMap(opts.Meta),
	}
	if prev != nil {
		if release.Env == nil && prev.Env != nil {
			release.Env = copyStringMap(prev.Env)
		}
		if release.Meta == nil && prev.Meta != nil {
			release.Meta = copyStringMap(prev.Meta)
		}
	}
	if release.Meta == nil {
		release.Meta = make(map[string]string)
	}
	for k, v := range opts.Meta {
		release.Meta[k] = v
	}

	proc := ct.ProcessType{}
	if prev != nil {
		if p, ok := prev.Processes[processName]; ok {
			proc = p
		}
	}
	if build != nil && len(build.Args) > 0 {
		proc.Args = append([]string{}, build.Args...)
	}
	if len(proc.Ports) == 0 {
		port := 8080
		if build != nil {
			port = build.ListenPort
		}
		service := &host.Service{
			Name:   appName + "-web",
			Create: true,
		}
		if opts.ServiceHealthCheck {
			service.Check = &host.HealthCheck{Type: "tcp"}
		}
		proc.Service = appName + "-web"
		proc.Ports = []ct.Port{{
			Port:    port,
			Proto:   "tcp",
			Service: service,
		}}
	}

	if build != nil && build.Manifest != nil {
		if ep := build.Manifest.DefaultEntrypoint(); ep != nil && ep.Env != nil {
			if release.Env == nil {
				release.Env = make(map[string]string)
			}
			for k, v := range ep.Env {
				if _, exists := release.Env[k]; !exists {
					release.Env[k] = v
				}
			}
		}
	}
	if build != nil && build.Config != nil {
		if release.Env == nil {
			release.Env = make(map[string]string, len(build.Config.Config.Env))
		}
		for _, v := range build.Config.Config.Env {
			keyVal := splitEnv(v)
			if keyVal == nil {
				continue
			}
			if _, exists := release.Env[keyVal[0]]; !exists {
				release.Env[keyVal[0]] = keyVal[1]
			}
		}
	}

	procs := make(map[string]ct.ProcessType, 1+len(opts.ExtraProcesses))
	procs[processName] = proc
	for name, p := range opts.ExtraProcesses {
		procs[name] = p
	}
	release.Processes = procs
	return release
}

// NewAppReleaseFromArtifact builds a release from a registered image artifact.
func NewAppReleaseFromArtifact(appName string, prev *ct.Release, artifact *ct.Artifact, opts ReleaseOptions) *ct.Release {
	build := &BuildResult{
		ListenPort: ListenPortFromArtifact(artifact),
	}
	if artifact != nil {
		if ep := artifact.Manifest().DefaultEntrypoint(); ep != nil {
			build.Args = append([]string{}, ep.Args...)
			build.Manifest = artifact.Manifest()
		}
	}
	return NewAppRelease(appName, prev, artifact.ID, build, opts)
}

// ListenPortFromArtifact returns the configured listen port for an imported image.
func ListenPortFromArtifact(artifact *ct.Artifact) int {
	if artifact != nil {
		if p, ok := artifact.Meta[MetaListenPort]; ok {
			if v, err := strconv.Atoi(p); err == nil && v > 0 {
				return v
			}
		}
	}
	return ListenPort(nil)
}

// ArtifactMeta returns metadata to attach when registering an imported image.
func ArtifactMeta(build *BuildResult) map[string]string {
	if build == nil {
		return nil
	}
	return map[string]string{
		MetaListenPort: strconv.Itoa(build.ListenPort),
	}
}

// BuildResultFromInspect constructs a BuildResult from docker inspect output.
func BuildResultFromInspect(entrypoint, cmd, env []string, exposed map[string]interface{}) *BuildResult {
	cfg := &Config{}
	cfg.Config.Entrypoint = append([]string{}, entrypoint...)
	cfg.Config.Cmd = append([]string{}, cmd...)
	cfg.Config.Env = append([]string{}, env...)
	cfg.Config.ExposedPorts = exposed
	return &BuildResult{
		Args:       append(append([]string{}, entrypoint...), cmd...),
		ListenPort: ListenPort(exposed),
		Config:     cfg,
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func splitEnv(v string) []string {
	for i := 0; i < len(v); i++ {
		if v[i] == '=' {
			return []string{v[:i], v[i+1:]}
		}
	}
	return nil
}
