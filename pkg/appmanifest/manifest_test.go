package appmanifest

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/plugin"
	router "github.com/randy-girard/flynn/router/types"
)

func TestParseDocsExamples(t *testing.T) {
	body, err := os.ReadFile("../../docs/content/app-manifest.md")
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	rest := string(body)
	for {
		i := strings.Index(rest, "```json\n")
		if i < 0 {
			break
		}
		rest = rest[i+len("```json\n"):]
		j := strings.Index(rest, "```")
		if j < 0 {
			t.Fatal("unclosed json fence")
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j:]
	}
	if len(blocks) != 2 {
		t.Fatalf("docs examples: %d", len(blocks))
	}
	m, err := Parse([]byte(blocks[0]))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Example" || m.EffectiveStack() != "heroku-24" {
		t.Fatalf("buildpack example: %+v", m)
	}
	if _, ok := m.Env["WEB_CONCURRENCY"]; !ok {
		t.Fatal("root env keeps WEB_CONCURRENCY")
	}
	preview := m.WithEnvironment("preview")
	if _, ok := preview.Env["WEB_CONCURRENCY"]; ok {
		t.Fatal("preview env must replace the root env and drop WEB_CONCURRENCY")
	}
	if preview.Env["APP_ENV"].Value != "preview-{{branch}}" {
		t.Fatalf("preview APP_ENV: %+v", preview.Env["APP_ENV"])
	}
	got := Substitute(preview.Env["APP_ENV"].Value, map[string]string{"branch": "topic"})
	if got != "preview-topic" {
		t.Fatal(got)
	}
	prod := m.WithEnvironment("production")
	if prod.Formation["web"].Quantity == nil || *prod.Formation["web"].Quantity != 2 {
		t.Fatal("production formation")
	}
	if _, ok := prod.Env["WEB_CONCURRENCY"]; !ok {
		t.Fatal("production overlay omits env, so the root env stays")
	}
	review := m.WithEnvironment("review")
	if _, ok := review.Env["WEB_CONCURRENCY"]; ok {
		t.Fatal("review maps to preview")
	}
	container, err := Parse([]byte(blocks[1]))
	if err != nil {
		t.Fatal(err)
	}
	if container.EffectiveStack() != "container" || container.Image == "" {
		t.Fatalf("container example: %+v", container)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []string{
		`{"strategy":"sirenia"}`,
		`{"strategy":"discoverd-meta"}`,
		`{"stack":"cedar"}`,
		`{"stack":"heroku-24","image":"example:latest"}`,
		`{"routes":[{"type":"http","domain":"example.com","path":"/admin"}]}`,
		`{"schema_version":2}`,
		`{"nope":true}`,
		`{"environments":{"test":{}}}`,
		`{"name":"this-name-is-longer-than-thirty"}`,
		`{"formation":{"web":{"runtime":"small"}}}`,
		`{"env":{"TOKEN":{"generator":"uuid"}}}`,
	}
	for _, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestSecretGenerator(t *testing.T) {
	m, err := Parse([]byte(`{"env":{"SECRET_TOKEN":{"generator":"secret"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ResolveEnv(m.Env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got["SECRET_TOKEN"]) != 64 {
		t.Fatalf("secret %q", got["SECRET_TOKEN"])
	}
	again, err := ResolveEnv(m.Env, got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again["SECRET_TOKEN"] != got["SECRET_TOKEN"] {
		t.Fatal("resume must keep the generated secret")
	}
	if _, err := ResolveEnv(map[string]EnvVar{"NEED": {}}, nil, nil); err == nil {
		t.Fatal("required env with no value must fail")
	}
}

func TestDecide(t *testing.T) {
	base := func() AppState {
		return AppState{App: &ct.App{Strategy: "all-at-once", DeployTimeout: 120, Meta: map[string]string{"gc.max_inactive_slug_releases": "10"}}}
	}
	fresh := base()
	if Decide(fresh, true) != ActionApply {
		t.Fatal("untouched app with a file applies")
	}
	if Decide(fresh, false) != ActionNone {
		t.Fatal("no file")
	}
	seed := base()
	seed.Head = &ct.Release{ID: "rel", Meta: map[string]string{MetaPreviewSeed: "true"}}
	if Decide(seed, true) != ActionApply {
		t.Fatal("preview seed release stays untouched")
	}
	configured := base()
	configured.Head = &ct.Release{ID: "rel"}
	if Decide(configured, true) != ActionSkip {
		t.Fatal("user release skips")
	}
	pending := base()
	pending.App.Meta = map[string]string{MetaStatus: StatusPending}
	pending.Head = &ct.Release{ID: "rel"}
	if Decide(pending, true) != ActionResume {
		t.Fatal("pending resumes even after a release exists")
	}
	applied := base()
	applied.App.Meta = map[string]string{MetaStatus: StatusApplied}
	if Decide(applied, true) != ActionSkip {
		t.Fatal("applied skips")
	}
	preview := base()
	preview.GitHubConnected = true
	preview.Environment = "preview"
	if Decide(preview, true) != ActionApply {
		t.Fatal("preview github connect does not skip")
	}
	linked := base()
	linked.GitHubConnected = true
	if Decide(linked, true) != ActionSkip {
		t.Fatal("github connect skips a non-preview app")
	}
	routed := base()
	routed.ExtraRoutes = 1
	if Decide(routed, true) != ActionSkip {
		t.Fatal("extra route skips")
	}
	if SkipLine != "-----> flynn-app.json present, app already configured; skipping" {
		t.Fatal(SkipLine)
	}
}

func TestArchiveInject(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("{}\n")
	hdr := &tar.Header{Name: "flynn-app.json", Mode: 0644, ModTime: time.Unix(0, 0), Size: int64(len(body))}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, hasBP, err := RootFiles(bytes.NewReader(buf.Bytes()))
	if err != nil || hasBP || string(manifest) != "{}\n" {
		t.Fatalf("manifest %q bp %v err %v", manifest, hasBP, err)
	}
	var out bytes.Buffer
	if err := InjectRootFile(bytes.NewReader(buf.Bytes()), &out, ".buildpacks", []byte("https://example.com/pack\n")); err != nil {
		t.Fatal(err)
	}
	_, hasBP, err = RootFiles(bytes.NewReader(out.Bytes()))
	if err != nil || !hasBP {
		t.Fatalf("inject bp %v err %v", hasBP, err)
	}
}

func TestApplyResumeAndPostdeploy(t *testing.T) {
	fake := newFakeBackend()
	log := &bytes.Buffer{}
	a := &Applier{
		b:       fake,
		Catalog: testCatalog(),
		Admin:   true,
		HTTPS:   true,
		Log:     log,
		Hooks: Hooks{
			GitHubInstalled: func() bool { return true },
			ConnectGitHub: func(id string, spec GitHub) error {
				fake.github = spec.Repository
				return nil
			},
		},
	}
	raw := []byte(`{
	  "description": "Example",
	  "env": {"SECRET_TOKEN": {"generator": "secret"}},
	  "formation": {"web": {"quantity": 2, "runtime": "small"}},
	  "addons": ["postgres", {"type": "mongodb", "as": "MONGO"}],
	  "scripts": {"postdeploy": "bundle exec rake bootstrap", "preview_destroy": "rake preview:destroy"},
	  "github": {"repository": "acme/example"}
	}`)
	res, err := a.Prepare("app", raw, PrepareOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if fake.provisions != 2 {
		t.Fatalf("provisions %d", fake.provisions)
	}
	if fake.apps["app"].Meta[MetaStatus] != StatusPending {
		t.Fatal(fake.apps["app"].Meta)
	}
	if len(res.Env["SECRET_TOKEN"]) != 64 || res.Env["DATABASE_URL"] == "" || res.Env["MONGO_URL"] == "" {
		t.Fatalf("env %#v", res.Env)
	}
	if fake.apps["app"].Meta[MetaPreviewDestroy] != "rake preview:destroy" {
		t.Fatal(fake.apps["app"].Meta[MetaPreviewDestroy])
	}
	// Crash after the add-ons were recorded. The next push must not provision again.
	again, err := a.Prepare("app", raw, PrepareOptions{ExistingEnv: res.Env})
	if err != nil {
		t.Fatal(err)
	}
	if fake.provisions != 2 {
		t.Fatalf("resume provisions %d", fake.provisions)
	}
	if again.Env["SECRET_TOKEN"] != res.Env["SECRET_TOKEN"] {
		t.Fatal("secret rotated on resume")
	}
	rel := &ct.Release{ID: "built", ArtifactIDs: []string{"slug"}, Env: again.Env, Processes: map[string]ct.ProcessType{"web": {}}}
	fake.failCommand = errors.New("postdeploy exited 1")
	err = a.Finish("app", FinishInput{Release: rel, Processes: rel.Processes})
	var post *PostdeployError
	if !errors.As(err, &post) {
		t.Fatalf("postdeploy error: %v", err)
	}
	if fake.apps["app"].Meta[MetaStatus] != StatusPending || fake.apps["app"].Meta[MetaPostdeploy] != PostdeployFailed {
		t.Fatal(fake.apps["app"].Meta)
	}
	if fake.deleted != 0 {
		t.Fatal("postdeploy failure must not delete the app")
	}
	fake.failCommand = nil
	if err := a.Finish("app", FinishInput{Release: rel, Processes: rel.Processes}); err != nil {
		t.Fatal(err)
	}
	if fake.commands != 1 {
		t.Fatalf("postdeploy ran %d times", fake.commands)
	}
	if fake.apps["app"].Meta[MetaStatus] != StatusApplied {
		t.Fatal(fake.apps["app"].Meta[MetaStatus])
	}
	last := fake.scales[len(fake.scales)-1]
	if len(fake.scales) == 0 || last.Processes["web"] != 2 {
		t.Fatalf("scale %#v", fake.scales)
	}
	if fake.github != "acme/example" {
		t.Fatal(fake.github)
	}
}

func TestGitHubAndTagsFailBeforeAddons(t *testing.T) {
	fake := newFakeBackend()
	a := &Applier{b: fake, Catalog: testCatalog(), Log: ioDiscard()}
	if _, err := a.Prepare("app", []byte(`{"github":{"repository":"acme/example"},"addons":["postgres"]}`), PrepareOptions{}); err == nil {
		t.Fatal("github without the plugin must fail")
	}
	if fake.provisions != 0 || metaValue(fake.apps["app"], MetaStatus) != "" {
		t.Fatalf("provisioned before failure: %d %v", fake.provisions, fake.apps["app"].Meta)
	}
	a.Hooks.GitHubInstalled = func() bool { return true }
	if _, err := a.Prepare("app", []byte(`{"formation":{"web":{"quantity":1,"tags":{"disk":"ssd"}}},"addons":["postgres"]}`), PrepareOptions{}); err == nil {
		t.Fatal("tags without admin must fail")
	}
	if fake.provisions != 0 {
		t.Fatal("tags provisioned an addon")
	}
}

func TestCreateRollback(t *testing.T) {
	fake := newFakeBackend()
	a := &Applier{b: fake, Catalog: testCatalog(), Log: ioDiscard()}
	app := fake.apps["app"]
	err := a.ApplyCreate(app, []byte(`{"strategy":"sirenia"}`), PrepareOptions{}, nil)
	if err == nil || fake.deleted != 1 {
		t.Fatalf("err %v deleted %d", err, fake.deleted)
	}
}

func testCatalog() *plugin.Catalog {
	return &plugin.Catalog{Addons: []plugin.AddonSpec{
		{Type: "postgres", Backend: plugin.BackendResource, Provider: "postgres", Verbs: []string{plugin.VerbCreate}},
		{Type: "mongodb", Backend: plugin.BackendResource, Provider: "mongodb", Verbs: []string{plugin.VerbCreate}},
		{Type: "redis", Backend: plugin.BackendResource, Provider: "redis", Verbs: []string{plugin.VerbCreate}},
	}}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }

type fakeBackend struct {
	apps        map[string]*ct.App
	head        *ct.Release
	provisions  int
	scales      []ct.ScaleOptions
	commands    int
	failCommand error
	deleted     int
	github      string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{apps: map[string]*ct.App{
		"app": {ID: "app", Name: "example", Strategy: "all-at-once", DeployTimeout: 120},
	}}
}

func (f *fakeBackend) GetApp(id string) (*ct.App, error) {
	app := f.apps[id]
	if app == nil {
		return nil, errors.New("missing app")
	}
	cp := *app
	cp.Meta = cloneStrings(app.Meta)
	return &cp, nil
}

func (f *fakeBackend) UpdateApp(app *ct.App) error {
	cur := f.apps[app.ID]
	if app.Strategy != "" {
		cur.Strategy = app.Strategy
	}
	if app.DeployTimeout != 0 {
		cur.DeployTimeout = app.DeployTimeout
	}
	return nil
}

func (f *fakeBackend) UpdateAppMeta(app *ct.App) error {
	cur := f.apps[app.ID]
	cur.Meta = cloneStrings(app.Meta)
	return nil
}

func (f *fakeBackend) DeleteApp(id string) error {
	f.deleted++
	delete(f.apps, id)
	return nil
}

func (f *fakeBackend) Head(string) (*ct.Release, error) {
	if f.head == nil {
		return nil, nil
	}
	cp := *f.head
	cp.Env = cloneStrings(f.head.Env)
	return &cp, nil
}

func (f *fakeBackend) SaveRelease(_ string, rel *ct.Release, _ bool) error {
	rel.ID = "rel"
	cp := *rel
	cp.Env = cloneStrings(rel.Env)
	f.head = &cp
	return nil
}

func (f *fakeBackend) Provision(req *ct.ResourceReq) (*ct.Resource, error) {
	f.provisions++
	env := map[string]string{"DATABASE_URL": "postgres://db", "FLYNN_POSTGRES": "postgresql-concave-48291"}
	if req.ProviderID == "mongodb" {
		env = map[string]string{"DATABASE_URL": "mongodb://db", "FLYNN_MONGODB": "mongodb-harbor-48291"}
	}
	return &ct.Resource{ID: req.ProviderID + "-1", Env: env}, nil
}

func (f *fakeBackend) PutPluginAddon(string, string) error { return nil }

func (f *fakeBackend) Routes(string) ([]*router.Route, error) { return nil, nil }

func (f *fakeBackend) CreateRoute(string, *router.Route) error { return nil }

func (f *fakeBackend) Scale(_ string, _ string, opts ct.ScaleOptions) error {
	f.scales = append(f.scales, opts)
	return nil
}

func (f *fakeBackend) Profiles() ([]*ct.RuntimeProfile, error) {
	return []*ct.RuntimeProfile{{Name: "small", Memory: 512 << 20, CPU: 500}}, nil
}

func (f *fakeBackend) Settings() (*ct.RuntimeSettings, error) {
	return &ct.RuntimeSettings{}, nil
}

func (f *fakeBackend) RunCommand(string, *ct.Release, string, resource.Resources) error {
	f.commands++
	return f.failCommand
}

func (f *fakeBackend) Formation(string, string) (*ct.Formation, error) { return nil, nil }

func (f *fakeBackend) Resources(string) ([]*ct.Resource, error) { return nil, nil }

func TestPlanBuildpacks(t *testing.T) {
	m := Manifest{Stack: "heroku-24", Buildpacks: []Buildpack{{URL: "https://example.com/a"}}}
	if PlanBuildpacks(m, false).URL != "https://example.com/a" {
		t.Fatal("one url")
	}
	if PlanBuildpacks(m, true).URL != "" {
		t.Fatal("repo file wins")
	}
	m.Buildpacks = append(m.Buildpacks, Buildpack{URL: "https://example.com/b"})
	lines := PlanBuildpacks(m, false).Lines
	if len(lines) != 2 {
		t.Fatal(lines)
	}
	_ = json.Valid
}
