package cli

import (
	"bytes"
	"reflect"
	"sort"
	"strings"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	"github.com/randy-girard/flynn/controller/tokensigner"
	ct "github.com/randy-girard/flynn/controller/types"
)

const (
	restoreTestClusterKey = "cluster-key-0123456789abcdef"
	restoreTestOtherKey   = "blobstore-own-key-fedcba9876543210"
	restoreTestBadToken   = "not-a-token-key"
)

func restoreTestTokenKeys(t *testing.T) (controllerPub, otherPub string) {
	t.Helper()
	pub, _, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := tokensigner.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	return pub, other
}

// A backup taken before the blobstore release carried keys restores a
// blobstore that refuses every object request (EDGE-12). The restore fills
// the missing keys from the restored controller release, never replaces a
// present valid key, and replaces only a token key that cannot be parsed.
func TestRestoreFillsMissingBlobstoreKeys(t *testing.T) {
	tokenKey, otherTokenKey := restoreTestTokenKeys(t)
	controllerEnv := map[string]string{
		"AUTH_KEY":                 restoreTestClusterKey,
		"ACCESS_TOKEN_KEY":         tokenKey,
		"ACCESS_TOKEN_SIGNING_KEY": "signing-secret-not-used",
	}
	src := restoreKeySourcesFrom(controllerEnv)

	for _, tc := range []struct {
		name     string
		env      map[string]string
		want     map[string]string
		filled   []string
		replaced []string
	}{
		{
			name:   "no keys",
			env:    map[string]string{"DISCOVERD_AUTH_KEY": "d"},
			want:   map[string]string{"DISCOVERD_AUTH_KEY": "d", "AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": tokenKey},
			filled: []string{"ACCESS_TOKEN_KEY", "AUTH_KEY"},
		},
		{
			name:   "only AUTH_KEY",
			env:    map[string]string{"AUTH_KEY": restoreTestOtherKey},
			want:   map[string]string{"AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": tokenKey},
			filled: []string{"ACCESS_TOKEN_KEY"},
		},
		{
			name:   "only CONTROLLER_KEY counts as the cluster key",
			env:    map[string]string{"CONTROLLER_KEY": restoreTestOtherKey},
			want:   map[string]string{"CONTROLLER_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": tokenKey},
			filled: []string{"ACCESS_TOKEN_KEY"},
		},
		{
			name:   "only ACCESS_TOKEN_KEY",
			env:    map[string]string{"ACCESS_TOKEN_KEY": otherTokenKey},
			want:   map[string]string{"AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": otherTokenKey},
			filled: []string{"AUTH_KEY"},
		},
		{
			name: "both present stay unchanged",
			env:  map[string]string{"AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": otherTokenKey},
			want: map[string]string{"AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": otherTokenKey},
		},
		{
			name:     "invalid ACCESS_TOKEN_KEY is replaced with the controller key",
			env:      map[string]string{"AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": restoreTestBadToken},
			want:     map[string]string{"AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": tokenKey},
			replaced: []string{"ACCESS_TOKEN_KEY"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := copyEnv(tc.env)
			got, res, err := fillRestoredKeys("blobstore", tc.env, src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.env, in) {
				t.Fatalf("input env was modified: %v", tc.env)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("env = %v, want %v", envNames(got), envNames(tc.want))
			}
			if !reflect.DeepEqual(sorted(res.Filled), sorted(tc.filled)) {
				t.Fatalf("filled = %v, want %v", res.Filled, tc.filled)
			}
			if !reflect.DeepEqual(sorted(res.Replaced), sorted(tc.replaced)) {
				t.Fatalf("replaced = %v, want %v", res.Replaced, tc.replaced)
			}
			// Idempotent: a second pass over the result changes nothing.
			again, res2, err := fillRestoredKeys("blobstore", got, src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, got) || res2.changed() {
				t.Fatalf("second fill changed the env: %v %+v", envNames(again), res2)
			}
			assertNoSecrets(t, strings.Join(append(res.Warnings, res2.Warnings...), "\n"), tc.env, controllerEnv)
		})
	}
}

// Without a cluster key to copy, the blobstore would refuse every object
// request, so the restore stops before starting it and names the key.
func TestRestoreFailsWhenBlobstoreClusterKeyCannotBeDerived(t *testing.T) {
	tokenKey, _ := restoreTestTokenKeys(t)
	src := restoreKeySourcesFrom(map[string]string{"ACCESS_TOKEN_KEY": tokenKey})
	_, _, err := fillRestoredKeys("blobstore", map[string]string{}, src)
	if err == nil {
		t.Fatal("expected an error for a blobstore with no derivable AUTH_KEY")
	}
	if !strings.Contains(err.Error(), "AUTH_KEY") || !strings.Contains(err.Error(), "blobstore") {
		t.Fatalf("error does not name the app and key: %v", err)
	}
}

// A missing or invalid token key with no valid controller key to copy is
// kept as it is with a warning: the cluster key still authenticates.
func TestRestoreWarnsWhenNoTokenKeyToCopy(t *testing.T) {
	src := restoreKeySourcesFrom(map[string]string{"AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": restoreTestBadToken})
	if src.AccessTokenKey != "" {
		t.Fatal("an invalid controller ACCESS_TOKEN_KEY must not be used as a source")
	}
	for _, env := range []map[string]string{{}, {"ACCESS_TOKEN_KEY": restoreTestBadToken}} {
		got, res, err := fillRestoredKeys("blobstore", env, src)
		if err != nil {
			t.Fatal(err)
		}
		if got["AUTH_KEY"] != restoreTestClusterKey {
			t.Fatal("AUTH_KEY not filled")
		}
		if got["ACCESS_TOKEN_KEY"] != env["ACCESS_TOKEN_KEY"] {
			t.Fatal("ACCESS_TOKEN_KEY changed without a valid source")
		}
		if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "ACCESS_TOKEN_KEY") {
			t.Fatalf("no ACCESS_TOKEN_KEY warning: %v", res.Warnings)
		}
		assertNoSecrets(t, strings.Join(res.Warnings, "\n"), env, map[string]string{"k": restoreTestClusterKey})
	}
}

// The cluster key copied is the first entry of the controller AUTH_KEY
// (falling back to CONTROLLER_KEY), the key cluster clients send.
func TestRestoreKeySourcesUseFirstControllerKey(t *testing.T) {
	if got := restoreKeySourcesFrom(map[string]string{"AUTH_KEY": " a , b"}).ClusterKey; got != "a" {
		t.Fatalf("ClusterKey = %q", got)
	}
	if got := restoreKeySourcesFrom(map[string]string{"CONTROLLER_KEY": "c"}).ClusterKey; got != "c" {
		t.Fatalf("ClusterKey = %q", got)
	}
}

// Same hazard for the other system apps the scheduler starts from their
// backed-up releases: logaggregator (CONTROLLER_KEY since EDGE-04), router
// (cluster key since EDGE-04) and tarreceive (exits without CONTROLLER_KEY,
// AUTH_KEY or with an unparsable ACCESS_TOKEN_KEY).
func TestRestoreFillsOtherSystemAppKeys(t *testing.T) {
	tokenKey, _ := restoreTestTokenKeys(t)
	src := restoreKeySourcesFrom(map[string]string{"AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": tokenKey})
	for _, tc := range []struct {
		app  string
		env  map[string]string
		want map[string]string
	}{
		{"logaggregator", map[string]string{}, map[string]string{"CONTROLLER_KEY": restoreTestClusterKey}},
		{"logaggregator", map[string]string{"AUTH_KEY": restoreTestOtherKey}, map[string]string{"AUTH_KEY": restoreTestOtherKey}},
		{"router", map[string]string{}, map[string]string{"CONTROLLER_KEY": restoreTestClusterKey}},
		{"router", map[string]string{"CONTROLLER_KEY": restoreTestOtherKey}, map[string]string{"CONTROLLER_KEY": restoreTestOtherKey}},
		{"tarreceive", map[string]string{}, map[string]string{"CONTROLLER_KEY": restoreTestClusterKey, "AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": tokenKey}},
		{"tarreceive", map[string]string{"CONTROLLER_KEY": restoreTestOtherKey, "AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": restoreTestBadToken},
			map[string]string{"CONTROLLER_KEY": restoreTestOtherKey, "AUTH_KEY": restoreTestOtherKey, "ACCESS_TOKEN_KEY": tokenKey}},
		{"controller", map[string]string{}, map[string]string{}},
	} {
		got, _, err := fillRestoredKeys(tc.app, tc.env, src)
		if err != nil {
			t.Fatalf("%s: %v", tc.app, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s %v: env = %v, want %v", tc.app, envNames(tc.env), envNames(got), envNames(tc.want))
		}
	}
}

type restoreKeysFakeClient struct {
	apps       map[string]*ct.App
	releases   map[string]*ct.Release // by release ID
	current    map[string]string      // app ID -> release ID
	formations map[string]*ct.Formation
	created    int
}

func (c *restoreKeysFakeClient) GetApp(name string) (*ct.App, error) {
	if a, ok := c.apps[name]; ok {
		return a, nil
	}
	return nil, controller.ErrNotFound
}

func (c *restoreKeysFakeClient) GetAppRelease(appID string) (*ct.Release, error) {
	if a, ok := c.apps[appID]; ok {
		appID = a.ID
	}
	id, ok := c.current[appID]
	if !ok {
		return nil, controller.ErrNotFound
	}
	r := *c.releases[id]
	r.Env = copyEnv(r.Env)
	return &r, nil
}

func (c *restoreKeysFakeClient) GetFormation(appID, releaseID string) (*ct.Formation, error) {
	if f, ok := c.formations[appID+"/"+releaseID]; ok {
		return f, nil
	}
	return nil, controller.ErrNotFound
}

func (c *restoreKeysFakeClient) CreateRelease(appID string, r *ct.Release) error {
	c.created++
	r.ID = "new-release"
	r.AppID = appID
	stored := *r
	stored.Env = copyEnv(r.Env)
	c.releases[r.ID] = &stored
	return nil
}

func (c *restoreKeysFakeClient) PutFormation(f *ct.Formation) error {
	c.formations[f.AppID+"/"+f.ReleaseID] = f
	return nil
}

func (c *restoreKeysFakeClient) SetAppRelease(appID, releaseID string) error {
	c.current[appID] = releaseID
	return nil
}

// The fill is applied as a new release: the formation moves to it, the old
// formation is scaled to zero (so the scheduler does not start the keyless
// release next to it), the app points at it, and only key names are printed.
func TestRestoreFillAppKeysCreatesNewRelease(t *testing.T) {
	tokenKey, _ := restoreTestTokenKeys(t)
	controllerEnv := map[string]string{"AUTH_KEY": restoreTestClusterKey, "ACCESS_TOKEN_KEY": tokenKey}
	c := &restoreKeysFakeClient{
		apps:     map[string]*ct.App{"blobstore": {ID: "app-1", Name: "blobstore"}},
		releases: map[string]*ct.Release{"old-release": {ID: "old-release", AppID: "app-1", ArtifactIDs: []string{"art"}, Env: map[string]string{"DISCOVERD_AUTH_KEY": "d"}}},
		current:  map[string]string{"app-1": "old-release"},
		formations: map[string]*ct.Formation{"app-1/old-release": {
			AppID: "app-1", ReleaseID: "old-release", Processes: map[string]int{"web": 2},
		}},
	}
	var out bytes.Buffer
	if err := restoreFillAppKeys(c, "blobstore", restoreKeySourcesFrom(controllerEnv), &out); err != nil {
		t.Fatal(err)
	}
	if c.current["app-1"] != "new-release" {
		t.Fatalf("app release = %q", c.current["app-1"])
	}
	nr := c.releases["new-release"]
	if nr.Env["AUTH_KEY"] != restoreTestClusterKey || nr.Env["ACCESS_TOKEN_KEY"] != tokenKey || nr.Env["DISCOVERD_AUTH_KEY"] != "d" {
		t.Fatalf("new release env names = %v", envNames(nr.Env))
	}
	if !reflect.DeepEqual(nr.ArtifactIDs, []string{"art"}) {
		t.Fatalf("artifacts = %v", nr.ArtifactIDs)
	}
	if f := c.formations["app-1/new-release"]; f == nil || f.Processes["web"] != 2 {
		t.Fatalf("new formation = %+v", f)
	}
	if f := c.formations["app-1/old-release"]; f.Processes["web"] != 0 {
		t.Fatalf("old formation still scaled: %+v", f)
	}
	if !strings.Contains(out.String(), "AUTH_KEY") || !strings.Contains(out.String(), "ACCESS_TOKEN_KEY") {
		t.Fatalf("output does not name the filled keys: %q", out.String())
	}
	assertNoSecrets(t, out.String(), controllerEnv)

	// A second run finds the keys and creates nothing.
	out.Reset()
	if err := restoreFillAppKeys(c, "blobstore", restoreKeySourcesFrom(controllerEnv), &out); err != nil {
		t.Fatal(err)
	}
	if c.created != 1 {
		t.Fatalf("second run created a release (%d)", c.created)
	}

	// An app that is not in the backup is skipped.
	if err := restoreFillAppKeys(c, "tarreceive", restoreKeySourcesFrom(controllerEnv), &out); err != nil {
		t.Fatal(err)
	}
}

func assertNoSecrets(t *testing.T, out string, envs ...map[string]string) {
	t.Helper()
	for _, env := range envs {
		for k, v := range env {
			if len(v) >= 4 && strings.Contains(out, v) {
				t.Fatalf("output contains the value of %s: %q", k, out)
			}
		}
	}
}

func copyEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

func envNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func sorted(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}
