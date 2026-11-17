package flynnyml

import (
	"encoding/json"
	"strings"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/dockerimage"
)

// BuildRelease builds the release for a container git push that has flynn.yml.
// One built image keeps a single artifact and no per-process artifact id.
// build.config is not copied into the release env. A Procfile is not an input.
func BuildRelease(appName string, prev *ct.Release, m *Manifest, byName map[string]*ct.Artifact, env, meta map[string]string, extra map[string]ct.ProcessType) (*ct.Release, error) {
	if m == nil {
		return nil, errString("flynn.yml is empty")
	}
	procsIn, err := m.Processes()
	if err != nil {
		return nil, err
	}
	plans, err := m.PlannedBuilds()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(plans))
	seen := map[string]struct{}{}
	for _, p := range plans {
		art := byName[p.Names[0]]
		if art == nil || art.ID == "" {
			return nil, errString("flynn.yml: missing image " + p.Names[0])
		}
		if _, ok := seen[art.ID]; ok {
			continue
		}
		seen[art.ID] = struct{}{}
		ids = append(ids, art.ID)
	}
	multi := len(seen) > 1
	rel := &ct.Release{
		ArtifactIDs: ids,
		Env:         copyMap(env),
		Meta:        copyMap(meta),
		Processes:   map[string]ct.ProcessType{},
	}
	for _, proc := range procsIn {
		art := byName[proc.Image]
		if art == nil {
			return nil, errString("flynn.yml: missing image " + proc.Image)
		}
		ep, cmd := imageCommand(art)
		args := CommandArgs(ep, cmd, proc.Shell, proc.Command, proc.Exec)
		pt := baseProc(prev, proc.Name)
		pt.Args = args
		pt.Command = displayCommand(proc, args)
		if proc.Name == "web" && len(pt.Ports) == 0 {
			pt.Service, pt.Ports = webService(appName, dockerimage.ListenPortFromArtifact(art))
		}
		if proc.Name != "web" {
			pt.Service = ""
			pt.Ports = nil
		}
		if multi {
			pt.Artifact = art.ID
		}
		copyImageEnv(rel, &pt, art, multi)
		rel.Processes[proc.Name] = pt
	}
	if m.Release != nil {
		name, err := m.ReleaseImage()
		if err != nil {
			return nil, err
		}
		art := byName[name]
		if art == nil {
			return nil, errString("flynn.yml: missing image " + name)
		}
		pt := baseProc(prev, ct.ProcessTypeRelease)
		pt.Args = append([]string{}, m.Release.Command...)
		pt.Command = strings.Join(pt.Args, " ")
		pt.Service = ""
		pt.Ports = nil
		if multi {
			pt.Artifact = art.ID
		}
		rel.Processes[ct.ProcessTypeRelease] = pt
	}
	for name, p := range extra {
		if _, ok := rel.Processes[name]; ok {
			continue
		}
		rel.Processes[name] = p
	}
	return rel, nil
}

func baseProc(prev *ct.Release, name string) ct.ProcessType {
	if prev != nil {
		if p, ok := prev.Processes[name]; ok {
			p.Args = nil
			p.Command = ""
			p.Artifact = ""
			return p
		}
	}
	return ct.ProcessType{RuntimeProfile: resource.ProfileSmall}
}

func displayCommand(proc RunProc, args []string) string {
	if proc.Shell != "" {
		return proc.Shell
	}
	if proc.Exec {
		return strings.Join(proc.Command, " ")
	}
	return strings.Join(args, " ")
}

func webService(appName string, port int) (string, []ct.Port) {
	if port <= 0 {
		port = 8080
	}
	name := appName + "-web"
	return name, []ct.Port{{
		Port:  port,
		Proto: "tcp",
		Service: &host.Service{
			Name:   name,
			Create: true,
			Check:  &host.HealthCheck{Type: "tcp"},
		},
	}}
}

func imageCommand(art *ct.Artifact) (ep, cmd []string) {
	if art == nil {
		return nil, nil
	}
	if art.Meta != nil {
		if raw, ok := art.Meta[dockerimage.MetaEntrypoint]; ok {
			_ = json.Unmarshal([]byte(raw), &ep)
			if raw, ok = art.Meta[dockerimage.MetaCmd]; ok {
				_ = json.Unmarshal([]byte(raw), &cmd)
			}
			return ep, cmd
		}
	}
	if e := art.Manifest().DefaultEntrypoint(); e != nil {
		return nil, append([]string{}, e.Args...)
	}
	return nil, nil
}

func copyImageEnv(rel *ct.Release, proc *ct.ProcessType, art *ct.Artifact, multi bool) {
	if art == nil {
		return
	}
	ep := art.Manifest().DefaultEntrypoint()
	if ep == nil {
		return
	}
	for k, v := range ep.Env {
		if rel.Env != nil {
			if _, ok := rel.Env[k]; ok {
				continue
			}
		}
		if !multi {
			if rel.Env == nil {
				rel.Env = map[string]string{}
			}
			rel.Env[k] = v
			continue
		}
		if proc.Env == nil {
			proc.Env = map[string]string{}
		}
		if _, ok := proc.Env[k]; !ok {
			proc.Env[k] = v
		}
	}
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

type errString string

func (e errString) Error() string { return string(e) }
