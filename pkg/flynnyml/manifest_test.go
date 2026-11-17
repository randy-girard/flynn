package flynnyml

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dockerimage"
)

const example = `
setup:
  addons:
    - plan: heroku-postgresql
      as: DATABASE
    - type: redis
  config:
    S3_BUCKET: my-example-bucket
build:
  docker:
    web: Dockerfile
    worker:
      dockerfile: worker/Dockerfile
      target: production
    release:
      dockerfile: Dockerfile
      target: builder
  config:
    RAILS_ENV: development
    FOO: bar
release:
  image: release
  command:
    - ./deployment-tasks.sh
run:
  web: bundle exec puma -C config/puma.rb
  worker: python myworker.py
  asset-syncer:
    command:
      - python
      - asset-syncer.py
    image: worker
`

func TestParseExample(t *testing.T) {
	m, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if m.Setup == nil || len(m.Setup.Addons) != 2 {
		t.Fatalf("addons %#v", m.Setup)
	}
	if m.Setup.Addons[0].Plan != "heroku-postgresql" || m.Setup.Addons[0].As != "DATABASE" {
		t.Fatalf("addon0 %#v", m.Setup.Addons[0])
	}
	if m.Setup.Addons[1].Type != "redis" {
		t.Fatalf("addon1 %#v", m.Setup.Addons[1])
	}
	if m.Setup.Config["S3_BUCKET"] != "my-example-bucket" {
		t.Fatalf("config %#v", m.Setup.Config)
	}
	if len(m.Build.Images) != 3 {
		t.Fatalf("images %#v", m.Build.Images)
	}
	web := m.Build.Images[0]
	if web.Name != "web" || web.Dockerfile != "Dockerfile" || web.Context != "." || web.Filename != "Dockerfile" {
		t.Fatalf("web %#v", web)
	}
	worker := m.Build.Images[1]
	if worker.Name != "worker" || worker.Dockerfile != "worker/Dockerfile" || worker.Target != "production" || worker.Context != "worker" || worker.Filename != "Dockerfile" {
		t.Fatalf("worker %#v", worker)
	}
	if m.Build.Config["FOO"] != "bar" || m.Build.Config["RAILS_ENV"] != "development" {
		t.Fatalf("build config %#v", m.Build.Config)
	}
	if m.Release == nil || m.Release.Image != "release" || len(m.Release.Command) != 1 || m.Release.Command[0] != "./deployment-tasks.sh" {
		t.Fatalf("release %#v", m.Release)
	}
	if err := m.Resolve(); err != nil {
		t.Fatal(err)
	}
	procs, err := m.Processes()
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 3 {
		t.Fatalf("procs %#v", procs)
	}
	if procs[0].Name != "web" || procs[0].Shell != "bundle exec puma -C config/puma.rb" || procs[0].Image != "web" {
		t.Fatalf("web run %#v", procs[0])
	}
	if procs[2].Name != "asset-syncer" || procs[2].Image != "worker" || len(procs[2].Command) != 2 {
		t.Fatalf("asset %#v", procs[2])
	}
	plans, err := m.PlannedBuilds()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans %d", len(plans))
	}
}

func TestRejectsNonStringBuildConfig(t *testing.T) {
	for _, body := range []string{
		"build:\n  docker:\n    web: Dockerfile\n  config:\n    FOO: yes\n",
		"build:\n  docker:\n    web: Dockerfile\n  config:\n    FOO: 1\n",
		"build:\n  docker:\n    web: Dockerfile\n  config:\n    FOO: true\n",
		"setup:\n  config:\n    FOO: yes\nbuild:\n  docker:\n    web: Dockerfile\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted non-string config:\n%s", body)
		}
	}
}

func TestRejectsPathTraversalAndMissingDocker(t *testing.T) {
	cases := []string{
		"build:\n  docker:\n    web: ../Dockerfile\n",
		"build:\n  docker:\n    web: /etc/Dockerfile\n",
		"build:\n  docker:\n    web:\n      dockerfile: worker/../../secret\n",
		"run:\n  web: echo hi\n",
		"build:\n  docker:\n    web:\n      target: prod\n",
		"build:\n  config:\n    FOO: bar\n",
	}
	for _, body := range cases {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted:\n%s", body)
		}
	}
}

func TestSharedDockerfileAndTargetIsOneBuild(t *testing.T) {
	body := []byte("build:\n  docker:\n    web: Dockerfile\n    worker:\n      dockerfile: Dockerfile\nrun:\n  web: echo a\n  worker:\n    command: [python, w.py]\n    image: web\n")
	m, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Resolve(); err != nil {
		t.Fatal(err)
	}
	plans, err := m.PlannedBuilds()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans %#v", plans)
	}
	if len(plans[0].Names) != 2 {
		t.Fatalf("names %#v", plans[0].Names)
	}
}

func TestTargetIsItsOwnBuild(t *testing.T) {
	body := []byte("build:\n  docker:\n    web: Dockerfile\n    release:\n      dockerfile: Dockerfile\n      target: builder\nrelease:\n  image: release\n  command: [./tasks.sh]\n")
	m, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := m.PlannedBuilds()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans %#v", plans)
	}
	var release *PlannedBuild
	for i := range plans {
		for _, n := range plans[i].Names {
			if n == "release" {
				release = &plans[i]
			}
		}
	}
	if release == nil || release.Target != "builder" {
		t.Fatalf("release plan %#v", release)
	}
}

func TestImageBoundAndSize(t *testing.T) {
	var b strings.Builder
	b.WriteString("build:\n  docker:\n")
	for i := 0; i < MaxImages+1; i++ {
		b.WriteString("    img" + string(rune('a'+i)) + ": d" + string(rune('a'+i)) + ".Dockerfile\n")
	}
	if _, err := Parse([]byte(b.String())); err == nil {
		t.Fatal("more than MaxImages must fail")
	}
	huge := bytes.Repeat([]byte("a"), MaxBytes+1)
	if _, err := Parse(huge); err == nil {
		t.Fatal("huge file must fail")
	}
}

func TestPlanMapping(t *testing.T) {
	known := func(name string) bool { return name == "elasticsearch" || name == "postgres" || name == "redis" }
	got, err := ResolveAddonType("heroku-postgresql:mini", "", known)
	if err != nil || got != "postgres" {
		t.Fatalf("pg %q %v", got, err)
	}
	got, err = ResolveAddonType("heroku-redis", "", known)
	if err != nil || got != "redis" {
		t.Fatalf("redis %q %v", got, err)
	}
	got, err = ResolveAddonType("openredis:premium", "", known)
	if err != nil || got != "redis" {
		t.Fatalf("openredis %q %v", got, err)
	}
	got, err = ResolveAddonType("mongolab", "", known)
	if err != nil || got != "mongodb" {
		t.Fatalf("mongo %q %v", got, err)
	}
	got, err = ResolveAddonType("jawsdb-maria:kite", "", known)
	if err != nil || got != "mysql" {
		t.Fatalf("maria %q %v", got, err)
	}
	got, err = ResolveAddonType("elasticsearch:mini", "", known)
	if err != nil || got != "elasticsearch" {
		t.Fatalf("custom %q %v", got, err)
	}
	if _, err := ResolveAddonType("no-such", "", known); err == nil {
		t.Fatal("unknown plan must fail")
	}
	if _, err := ResolveAddonType("heroku-postgresql", "redis", known); err == nil {
		t.Fatal("plan and type must agree")
	}
	if _, err := ResolveAddonType("", "", known); err == nil {
		t.Fatal("addon needs a plan or type")
	}
}

func TestForbiddenConfigKeys(t *testing.T) {
	err := ConfigCollides(map[string]string{"DATABASE_URL": "x"}, []string{"postgres"}, []string{"DATABASE"})
	if err == nil {
		t.Fatal("DATABASE_URL must be rejected")
	}
	err = ConfigCollides(map[string]string{"REDIS_URL": "x"}, []string{"redis"}, nil)
	if err == nil {
		t.Fatal("REDIS_URL must be rejected")
	}
	err = ConfigCollides(map[string]string{"DATABASE_URL": "x"}, []string{"postgres"}, nil)
	if err == nil {
		t.Fatal("conventional URL must be rejected")
	}
	if err := ConfigCollides(map[string]string{"S3_BUCKET": "b"}, []string{"postgres"}, []string{"DATABASE"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunImageResolution(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\nrun:\n  worker: python w.py\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Resolve(); err != nil {
		t.Fatal(err)
	}
	procs, err := m.Processes()
	if err != nil || len(procs) != 1 || procs[0].Image != "web" {
		t.Fatalf("procs %#v err %v", procs, err)
	}
	bad, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\n    worker: worker/Dockerfile\nrun:\n  clock: python c.py\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bad.Resolve(); err == nil {
		t.Fatal("missing image must fail before the build")
	}
}

func TestOmittedRunUsesBuiltImagesExceptRelease(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\n    worker: worker/Dockerfile\n    release:\n      dockerfile: rel/Dockerfile\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Resolve(); err != nil {
		t.Fatal(err)
	}
	procs, err := m.Processes()
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 2 || procs[0].Name != "web" || procs[1].Name != "worker" {
		t.Fatalf("procs %#v", procs)
	}
}

func TestReleaseImageDefault(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\nrelease:\n  command: [./tasks.sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := m.ReleaseImage()
	if err != nil || img != "web" {
		t.Fatalf("image %q %v", img, err)
	}
	only, err := Parse([]byte("build:\n  docker:\n    worker: Dockerfile\nrelease:\n  command: [./tasks.sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	img, err = only.ReleaseImage()
	if err != nil || img != "worker" {
		t.Fatalf("only %q %v", img, err)
	}
	many, err := Parse([]byte("build:\n  docker:\n    worker: Dockerfile\n    clock: clock/Dockerfile\nrelease:\n  command: [./tasks.sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := many.ReleaseImage(); err == nil {
		t.Fatal("ambiguous release image must fail")
	}
}

func TestCommandArgs(t *testing.T) {
	shell := CommandArgs([]string{"/docker-entrypoint.sh"}, []string{"old"}, "bundle exec puma", nil, false)
	if strings.Join(shell, " ") != "/docker-entrypoint.sh sh -c bundle exec puma" {
		t.Fatalf("scalar %#v", shell)
	}
	bare := CommandArgs(nil, []string{"/app"}, "echo hi", nil, false)
	if strings.Join(bare, " ") != "sh -c echo hi" {
		t.Fatalf("bare %#v", bare)
	}
	consumed := CommandArgs([]string{"/bin/sh", "-c"}, []string{"old"}, "echo hi", nil, false)
	if strings.Join(consumed, " ") != "/bin/sh -c echo hi" {
		t.Fatalf("consumed %#v", consumed)
	}
	execd := CommandArgs([]string{"/docker-entrypoint.sh"}, []string{"old"}, "", []string{"python", "asset-syncer.py"}, true)
	if strings.Join(execd, " ") != "/docker-entrypoint.sh python asset-syncer.py" {
		t.Fatalf("exec %#v", execd)
	}
	execShell := CommandArgs([]string{"/bin/sh", "-c", "old"}, nil, "", []string{"python", "x.py"}, true)
	if strings.Join(execShell, " ") != "python x.py" {
		t.Fatalf("exec shell %#v", execShell)
	}
	kept := CommandArgs([]string{"/docker-entrypoint.sh"}, []string{"postgres"}, "", nil, false)
	if strings.Join(kept, " ") != "/docker-entrypoint.sh postgres" {
		t.Fatalf("image cmd %#v", kept)
	}
}

func TestBuildSpecDoesNotUseRootContextForNestedDockerfile(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    worker:\n      dockerfile: worker/Dockerfile\n      target: production\n  config:\n    FOO: bar\n"))
	if err != nil {
		t.Fatal(err)
	}
	plans, err := m.PlannedBuilds()
	if err != nil {
		t.Fatal(err)
	}
	spec, err := EncodeBuildSpec(plans, map[string]string{plans[0].Key(): "art-worker"}, m.Build.Config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(spec, "echo ") {
		t.Fatal("spec must not be a shell log line")
	}
	if !strings.Contains(spec, "worker") || !strings.Contains(spec, "production") || !strings.Contains(spec, "FOO") || !strings.Contains(spec, "bar") || !strings.Contains(spec, "art-worker") {
		t.Fatalf("spec %q", spec)
	}
}

func TestOneImageKeepsSingleArtifact(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\n  config:\n    FOO: secret\nrun:\n  web: bundle exec puma\n  worker:\n    command: [python, w.py]\n    image: web\n"))
	if err != nil {
		t.Fatal(err)
	}
	img := testArtifact(t, "art-web", []string{"/docker-entrypoint.sh"}, []string{"old"}, "8080", "layer-web")
	rel, err := BuildRelease("myapp", nil, m, map[string]*ct.Artifact{"web": img}, map[string]string{"DATABASE_URL": "postgres://db"}, map[string]string{"git": "true"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.ArtifactIDs) != 1 || rel.ArtifactIDs[0] != "art-web" {
		t.Fatalf("artifacts %#v", rel.ArtifactIDs)
	}
	if rel.Env["FOO"] != "" {
		t.Fatalf("build arg leaked into env: %#v", rel.Env)
	}
	if rel.Env["DATABASE_URL"] != "postgres://db" {
		t.Fatalf("env %#v", rel.Env)
	}
	web := rel.Processes["web"]
	worker := rel.Processes["worker"]
	if web.Artifact != "" || worker.Artifact != "" {
		t.Fatalf("one image must not set per-process artifact web=%q worker=%q", web.Artifact, worker.Artifact)
	}
	if web.Command != "bundle exec puma" || !strings.Contains(strings.Join(web.Args, " "), "bundle exec puma") {
		t.Fatalf("web %#v", web)
	}
	if strings.Join(worker.Args, " ") != "/docker-entrypoint.sh python w.py" {
		t.Fatalf("worker args %#v", worker.Args)
	}
	if _, ok := rel.Processes["release"]; ok {
		t.Fatal("Procfile release must not appear; flynn.yml omitted release")
	}
	if len(web.Ports) != 1 || web.Ports[0].Port != 8080 || web.Service != "myapp-web" {
		t.Fatalf("web service %#v", web)
	}
	if len(worker.Ports) != 0 || worker.Service != "" {
		t.Fatalf("worker must not get the route %#v", worker)
	}
}

func TestTwoImagesUseTwoArtifacts(t *testing.T) {
	m, err := Parse([]byte("build:\n  docker:\n    web: Dockerfile\n    worker: worker/Dockerfile\n    release:\n      dockerfile: Dockerfile\n      target: builder\nrun:\n  web: echo web\n  worker: echo worker\nrelease:\n  image: release\n  command: [./deployment-tasks.sh]\n"))
	if err != nil {
		t.Fatal(err)
	}
	web := testArtifact(t, "art-web", []string{"/entry"}, []string{"web"}, "8080", "layer-web")
	worker := testArtifact(t, "art-worker", []string{"/entry"}, []string{"worker"}, "8080", "layer-worker")
	releaseImg := testArtifact(t, "art-rel", []string{"/entry"}, []string{"rel"}, "8080", "layer-rel")
	rel, err := BuildRelease("myapp", nil, m, map[string]*ct.Artifact{
		"web": web, "worker": worker, "release": releaseImg,
	}, map[string]string{"DATABASE_URL": "postgres://db"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.ArtifactIDs) != 3 {
		t.Fatalf("artifacts %#v", rel.ArtifactIDs)
	}
	seen := map[string]bool{}
	for _, id := range rel.ArtifactIDs {
		seen[id] = true
	}
	if !seen["art-web"] || !seen["art-worker"] || !seen["art-rel"] {
		t.Fatalf("artifacts %#v", rel.ArtifactIDs)
	}
	if rel.Processes["web"].Artifact != "art-web" || rel.Processes["worker"].Artifact != "art-worker" {
		t.Fatalf("web %#v worker %#v", rel.Processes["web"], rel.Processes["worker"])
	}
	relProc := rel.Processes["release"]
	if strings.Join(relProc.Args, " ") != "./deployment-tasks.sh" || relProc.Artifact != "art-rel" {
		t.Fatalf("release %#v", relProc)
	}
	if len(relProc.Ports) != 0 {
		t.Fatal("release is not a service")
	}
	job := ct.ReleasePhaseNewJob(rel)
	if job == nil || strings.Join(job.Args, " ") != "./deployment-tasks.sh" {
		t.Fatalf("release job %#v", job)
	}
	if rel.Env["DATABASE_URL"] != "postgres://db" {
		t.Fatal("release env must include addon URLs")
	}
}

func TestSymlinkEscapeAndAppJSON(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTar(t, tw, &tar.Header{Name: "flynn.yml", Mode: 0644, Size: int64(len("build:\n  docker:\n    web: Dockerfile\n"))}, []byte("build:\n  docker:\n    web: Dockerfile\n"))
	writeTar(t, tw, &tar.Header{Name: "flynn-app.json", Mode: 0644, Size: 3}, []byte("{}\n"))
	writeTar(t, tw, &tar.Header{Name: "Dockerfile", Mode: 0644, Size: 6}, []byte("FROM \n"))
	writeTar(t, tw, &tar.Header{Name: "escape", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, nil)
	writeTar(t, tw, &tar.Header{Name: "inside", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "Dockerfile"}, nil)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	tree, err := ReadTree(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !tree.HasAppJSON || !bytes.Contains(tree.FlynnYml, []byte("Dockerfile")) {
		t.Fatalf("tree app=%v yml=%q", tree.HasAppJSON, tree.FlynnYml)
	}
	if err := tree.CheckPath("Dockerfile"); err != nil {
		t.Fatal(err)
	}
	if err := tree.CheckPath("inside"); err != nil {
		t.Fatal(err)
	}
	if err := tree.CheckPath("escape"); err == nil {
		t.Fatal("symlink escape must fail")
	}
	if err := tree.CheckPath("../Dockerfile"); err == nil {
		t.Fatal("traversal must fail")
	}
}

func TestFlynnYmlSymlinkEscape(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeTar(t, tw, &tar.Header{Name: "flynn.yml", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/tmp/secret.yml"}, nil)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTree(bytes.NewReader(buf.Bytes())); err == nil {
		t.Fatal("flynn.yml symlink that leaves the repo must fail")
	}
}

func TestStackRequiredMessage(t *testing.T) {
	if !strings.Contains(StackRequired, "flynn.yml requires the container stack") || !strings.Contains(StackRequired, "flynn stack:set container") {
		t.Fatal(StackRequired)
	}
	if SetupIgnoredLine != "-----> flynn.yml setup ignored; flynn-app.json already describes this app" {
		t.Fatal(SetupIgnoredLine)
	}
}

func testArtifact(t *testing.T, id string, ep, cmd []string, port, layer string) *ct.Artifact {
	t.Helper()
	manifest := &ct.ImageManifest{
		Type: ct.ImageManifestTypeV1,
		Entrypoints: map[string]*ct.ImageEntrypoint{
			"_default": {Args: append(append([]string{}, ep...), cmd...)},
		},
		Rootfs: []*ct.ImageRootfs{{
			Layers: []*ct.ImageLayer{{ID: layer, Type: ct.ImageLayerTypeSquashfs, Length: 1}},
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	epj, _ := json.Marshal(ep)
	cmdj, _ := json.Marshal(cmd)
	return &ct.Artifact{
		ID:          id,
		Type:        ct.ArtifactTypeFlynn,
		RawManifest: raw,
		Meta: map[string]string{
			dockerimage.MetaListenPort: port,
			dockerimage.MetaEntrypoint: string(epj),
			dockerimage.MetaCmd:        string(cmdj),
		},
	}
}

func writeTar(t *testing.T, tw *tar.Writer, hdr *tar.Header, body []byte) {
	t.Helper()
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
}
