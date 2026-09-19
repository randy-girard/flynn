package utils

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/stream"
)

func flynnArtifact(t *testing.T, entrypoints map[string]*ct.ImageEntrypoint, layers []*ct.ImageLayer) *ct.Artifact {
	t.Helper()
	m := &ct.ImageManifest{
		Type:        ct.ImageManifestTypeV1,
		Entrypoints: entrypoints,
	}
	if layers != nil {
		m.Rootfs = []*ct.ImageRootfs{{Layers: layers}}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &ct.Artifact{
		ID:               "art-1",
		Type:             ct.ArtifactTypeFlynn,
		RawManifest:      raw,
		LayerURLTemplate: "http://blobstore.discoverd/layers/{id}.squashfs",
		Meta:             map[string]string{"blobstore": "true"},
	}
}

func TestProcessArgsPrefersImageWhenProcessIsSuffix(t *testing.T) {
	image := []string{"docker-php-entrypoint", "bin/start-web"}
	proc := []string{"bin/start-web"}
	got := processArgs(proc, image)
	if strings.Join(got, " ") != strings.Join(image, " ") {
		t.Fatalf("got %v", got)
	}
	if got := processArgs(nil, image); strings.Join(got, " ") != strings.Join(image, " ") {
		t.Fatalf("empty process: %v", got)
	}
	if got := processArgs(proc, nil); strings.Join(got, " ") != strings.Join(proc, " ") {
		t.Fatalf("empty image: %v", got)
	}
	explicit := []string{"/custom"}
	if got := processArgs(explicit, image); strings.Join(got, " ") != "/custom" {
		t.Fatalf("explicit process: %v", got)
	}
}

func TestGetEntrypointUsesTopOverlayThenDefault(t *testing.T) {
	base := flynnArtifact(t, map[string]*ct.ImageEntrypoint{
		"_default": {Args: []string{"/base"}},
		"web":      {Args: []string{"/base-web"}},
	}, nil)
	top := flynnArtifact(t, map[string]*ct.ImageEntrypoint{
		"web": {Args: []string{"/top-web"}},
	}, nil)
	docker := &ct.Artifact{Type: ct.DeprecatedArtifactTypeDocker}
	got := GetEntrypoint([]*ct.Artifact{base, docker, top}, "web")
	if got == nil || strings.Join(got.Args, " ") != "/top-web" {
		t.Fatalf("typed entrypoint: %+v", got)
	}
	got = GetEntrypoint([]*ct.Artifact{base, docker}, "worker")
	if got == nil || strings.Join(got.Args, " ") != "/base" {
		t.Fatalf("default entrypoint: %+v", got)
	}
	if GetEntrypoint([]*ct.Artifact{docker}, "web") != nil {
		t.Fatal("docker artifacts have no flynn entrypoints")
	}
}

func TestSetupMountspecsSkipsNonFlynnAndNonSquashfs(t *testing.T) {
	job := &host.Job{}
	art := flynnArtifact(t, nil, []*ct.ImageLayer{
		{ID: "skip-tar", Type: "application/vnd.flynn.image.tar.v1", Length: 1},
		{ID: "layer-a", Type: ct.ImageLayerTypeSquashfs, Length: 12, Hashes: map[string]string{"sha512_256": "abc"}},
	})
	SetupMountspecs(job, []*ct.Artifact{
		{Type: ct.DeprecatedArtifactTypeDocker},
		art,
		flynnArtifact(t, nil, nil),
	})
	if len(job.Mountspecs) != 1 || job.Mountspecs[0].ID != "layer-a" {
		t.Fatalf("mountspecs=%+v", job.Mountspecs)
	}
	if job.Mountspecs[0].URL != "http://blobstore.discoverd/layers/layer-a.squashfs" {
		t.Fatalf("url=%s", job.Mountspecs[0].URL)
	}
}

func TestJobConfigSystemPartitionEnvAndDeprecatedArgs(t *testing.T) {
	uid := uint32(1000)
	art := flynnArtifact(t, map[string]*ct.ImageEntrypoint{
		"_default": {Env: map[string]string{"FROM_IMAGE": "1"}, WorkingDir: "/app", Uid: &uid, Args: []string{"/img"}},
	}, nil)
	f := &ct.ExpandedFormation{
		App: &ct.App{
			ID:   "app-1",
			Name: "demo",
			Meta: map[string]string{"flynn-system-app": "true", "keep": "me"},
		},
		Release: &ct.Release{
			ID:  "rel-1",
			Env: map[string]string{"FOO": "release", "FROM_IMAGE": "2"},
			Processes: map[string]ct.ProcessType{
				"web": {
					Env:               map[string]string{"FOO": "proc"},
					LinuxCapabilities: []string{"NET_BIND_SERVICE"},
					Ports:             []ct.Port{{Proto: "tcp", Port: 8080}},
					HostNetwork:       true,
				},
			},
		},
		Artifacts: []*ct.Artifact{art},
	}
	job := JobConfig(f, "web", "host1", "job-uuid")
	if job.ID != "host1-job-uuid" {
		t.Fatalf("id=%s", job.ID)
	}
	if job.Partition != "system" {
		t.Fatal("system apps must land on the system partition")
	}
	if job.Config.Env["FOO"] != "proc" || job.Config.Env["FROM_IMAGE"] != "2" {
		t.Fatalf("env merge: %+v", job.Config.Env)
	}
	if job.Config.Env["FLYNN_APP_ID"] != "app-1" || job.Config.Env["FLYNN_JOB_ID"] != job.ID {
		t.Fatalf("flynn env: %+v", job.Config.Env)
	}
	if job.Metadata["keep"] != "me" || job.Metadata["flynn-controller.app"] != "app-1" {
		t.Fatalf("metadata: %+v", job.Metadata)
	}
	if job.Config.LinuxCapabilities == nil || (*job.Config.LinuxCapabilities)[0] != "NET_BIND_SERVICE" {
		t.Fatal("capabilities")
	}
	f.Release.Processes["web"] = ct.ProcessType{
		RuntimeProfile: "large",
		Args:           []string{"web"},
	}
	profiled := JobConfig(f, "web", "host1", "job-uuid")
	if profiled.Metadata["flynn-controller.runtime_profile"] != "large" {
		t.Fatalf("runtime_profile metadata=%v", profiled.Metadata)
	}
	if !job.Config.HostNetwork || len(job.Config.Ports) != 1 || job.Config.Ports[0].Port != 8080 {
		t.Fatalf("ports/host network: %+v", job.Config)
	}

	legacy := &ct.ExpandedFormation{
		App: &ct.App{ID: "a", Name: "n", Meta: map[string]string{}},
		Release: &ct.Release{
			ID: "r",
			Processes: map[string]ct.ProcessType{
				"web": {DeprecatedEntrypoint: []string{"/old"}, DeprecatedCmd: []string{"start"}},
			},
		},
	}
	got := JobConfig(legacy, "web", "h", "u")
	if strings.Join(got.Config.Args, " ") != "/old start" {
		t.Fatalf("deprecated args: %v", got.Config.Args)
	}
}

func TestJobMetaFromMetadataStripsControllerKeys(t *testing.T) {
	got := JobMetaFromMetadata(map[string]string{
		"flynn-controller.app": "secret-id",
		"keep":                 "yes",
	})
	if _, ok := got["flynn-controller.app"]; ok {
		t.Fatal("controller metadata must not leak into job meta")
	}
	if got["keep"] != "yes" {
		t.Fatalf("%v", got)
	}
}

func TestFormationTagsEqualAndAppName(t *testing.T) {
	a := map[string]map[string]string{"web": {"disk": "ssd"}}
	if !FormationTagsEqual(a, a) {
		t.Fatal("equal")
	}
	if FormationTagsEqual(a, map[string]map[string]string{"web": {"disk": "hdd"}}) {
		t.Fatal("different values")
	}
	if FormationTagsEqual(a, map[string]map[string]string{"web": {"disk": "ssd"}, "worker": {}}) {
		t.Fatal("different length")
	}
	if !AppNamePattern.MatchString("upgrade-smoke") || AppNamePattern.MatchString("Bad_Name") {
		t.Fatal("app name pattern")
	}
}

type expandStub struct {
	app      *ct.App
	release  *ct.Release
	artifact *ct.Artifact
}

func (s expandStub) GetApp(string) (*ct.App, error) {
	if s.app == nil {
		return nil, errors.New("no app")
	}
	return s.app, nil
}
func (s expandStub) GetRelease(string) (*ct.Release, error) {
	if s.release == nil {
		return nil, errors.New("no release")
	}
	return s.release, nil
}
func (s expandStub) GetArtifact(string) (*ct.Artifact, error) {
	if s.artifact == nil {
		return nil, errors.New("no artifact")
	}
	return s.artifact, nil
}
func (expandStub) GetExpandedFormation(string, string) (*ct.ExpandedFormation, error) {
	return nil, errors.New("unused")
}
func (expandStub) CreateApp(*ct.App) error                 { return nil }
func (expandStub) CreateRelease(string, *ct.Release) error { return nil }
func (expandStub) CreateArtifact(*ct.Artifact) error       { return nil }
func (expandStub) PutFormation(*ct.Formation) error        { return nil }
func (expandStub) PutScaleRequest(*ct.ScaleRequest) error  { return nil }
func (expandStub) StreamFormations(*time.Time, chan<- *ct.ExpandedFormation) (stream.Stream, error) {
	return nil, errors.New("unused")
}
func (expandStub) AppList() ([]*ct.App, error) { return nil, nil }
func (expandStub) FormationListActive() ([]*ct.ExpandedFormation, error) {
	return nil, nil
}
func (expandStub) SetAppRelease(string, string) error { return nil }
func (expandStub) PutJob(*ct.Job) error               { return nil }
func (expandStub) JobListActive() ([]*ct.Job, error)  { return nil, nil }
func (expandStub) StreamSinks(*time.Time, chan *ct.Sink) (stream.Stream, error) {
	return nil, errors.New("unused")
}
func (expandStub) ListSinks() ([]*ct.Sink, error)    { return nil, nil }
func (expandStub) VolumeList() ([]*ct.Volume, error) { return nil, nil }
func (expandStub) PutVolume(*ct.Volume) error        { return nil }
func (expandStub) StreamVolumes(*time.Time, chan *ct.Volume) (stream.Stream, error) {
	return nil, errors.New("unused")
}

func TestExpandFormation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	f := &ct.Formation{AppID: "app-1", ReleaseID: "rel-1", Processes: map[string]int{"web": 1}, UpdatedAt: &now}
	stub := expandStub{
		app:      &ct.App{ID: "app-1", Name: "demo"},
		release:  &ct.Release{ID: "rel-1", ArtifactIDs: []string{"art-1"}},
		artifact: &ct.Artifact{ID: "art-1"},
	}
	ef, err := ExpandFormation(stub, f)
	if err != nil {
		t.Fatal(err)
	}
	if ef.App.Name != "demo" || ef.Release.ID != "rel-1" || len(ef.Artifacts) != 1 || ef.Processes["web"] != 1 {
		t.Fatalf("%+v", ef)
	}
	if !ef.UpdatedAt.Equal(now) {
		t.Fatalf("updated at %v", ef.UpdatedAt)
	}
	if _, err := ExpandFormation(expandStub{}, f); err == nil {
		t.Fatal("missing app must fail")
	}
}

func TestFormationKeyString(t *testing.T) {
	k := NewFormationKey("a", "r")
	if k.String() != "a:r" {
		t.Fatalf("%s", k)
	}
}
