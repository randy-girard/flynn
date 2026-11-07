package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

func autoTags() map[string]string {
	return map[string]string{
		ct.FormationMaxProcessesTag:       ct.HostMaxProcessesAuto,
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	}
}

func TestResolveAutoUsesBuiltinProfilesWhenControllerIsDown(t *testing.T) {
	const gib = int64(1 << 30)
	builtin := BuiltinAppMemoryLimits()
	want := ct.DefaultHostMaxProcesses(8*gib, builtin, 253)
	if want < 1 {
		t.Fatal("builtin cap")
	}
	// The builtin table is the input. A hardcoded 512 MiB slot is not.
	var smallest int64
	for _, p := range resource.BuiltinProfiles() {
		if smallest == 0 || p.Memory < smallest {
			smallest = p.Memory
		}
	}
	if got := ct.DefaultHostMaxProcesses(8*gib, []int64{smallest}, 253); got != want {
		t.Fatalf("builtin helper drifted from resource.BuiltinProfiles: %d vs %d", got, want)
	}
	next, ok := ResolveAutoHostMaxProcesses(autoTags(), 8*gib, 253, nil, false, builtin)
	if !ok || next[ct.FormationMaxProcessesTag] != ct.EncodeHostMaxProcesses(want) {
		t.Fatalf("ok=%t tags=%v want %d", ok, next, want)
	}
	if next[ct.FormationMaxProcessesSourceTag] != ct.HostMaxProcessesSourceAuto {
		t.Fatalf("source %q", next[ct.FormationMaxProcessesSourceTag])
	}
}

func TestResolveAutoFollowsLiveCatalogNotPreviousSize(t *testing.T) {
	const gib = int64(1 << 30)
	tags := map[string]string{
		ct.FormationMaxProcessesTag:       "16",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
		"role":                            "web",
	}
	// Smallest live runtime is 2 GiB, so 8 GiB is 4, not the stored 16.
	live := []int64{2 * gib, 8 * gib}
	next, ok := ResolveAutoHostMaxProcesses(tags, 8*gib, 253, live, true, []int64{512 << 20})
	if !ok || next[ct.FormationMaxProcessesTag] != "4" || next["role"] != "web" {
		t.Fatalf("ok=%t tags=%v", ok, next)
	}
	if next[ct.FormationMaxProcessesSourceTag] != ct.HostMaxProcessesSourceAuto {
		t.Fatalf("source %v", next)
	}
}

func TestResolveAutoIncludesDatabaseRuntimes(t *testing.T) {
	const gib = int64(1 << 30)
	memories := RuntimeMemoryLimits(
		[]*ct.RuntimeProfile{{Name: "large", Memory: 2 * gib}},
		&dbruntime.Catalog{Runtimes: []dbruntime.Runtime{{Engine: "redis", Name: "small", Memory: 256 << 20}}},
	)
	if got := ct.DefaultHostMaxProcesses(8*gib, memories, 253); got != 32 {
		t.Fatalf("redis 256MiB must be the slot: got %d want 32", got)
	}
}

func TestResolveAutoLeavesOperatorAndLegacyCaps(t *testing.T) {
	const gib = int64(1 << 30)
	live := []int64{256 << 20}
	operator := map[string]string{
		ct.FormationMaxProcessesTag:       "1",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceOperator,
		"role":                            "database",
	}
	if _, ok := ResolveAutoHostMaxProcesses(operator, 8*gib, 253, live, true, nil); ok {
		t.Fatal("operator cap must not move")
	}
	legacy := map[string]string{ct.FormationMaxProcessesTag: "4", "role": "web"}
	if _, ok := ResolveAutoHostMaxProcesses(legacy, 8*gib, 253, live, true, nil); ok {
		t.Fatal("numeric tag without a source is operator")
	}
	if _, ok := ResolveAutoHostMaxProcesses(map[string]string{"role": "web"}, 8*gib, 253, live, true, nil); ok {
		t.Fatal("a host with no cap must not be backfilled")
	}
}

func TestResolveAutoKeepsNumberWhenControllerIsDown(t *testing.T) {
	tags := map[string]string{
		ct.FormationMaxProcessesTag:       "9",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	}
	if _, ok := ResolveAutoHostMaxProcesses(tags, 8<<30, 253, nil, false, []int64{512 << 20}); ok {
		t.Fatal("controller down must keep the resolved number")
	}
}

func TestResolveAutoSkipsEmptyLiveCatalog(t *testing.T) {
	tags := map[string]string{
		ct.FormationMaxProcessesTag:       "9",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	}
	if _, ok := ResolveAutoHostMaxProcesses(tags, 8<<30, 253, nil, true, []int64{512 << 20}); ok {
		t.Fatal("empty live catalog must not fall back to builtins over a number")
	}
	if _, ok := ResolveAutoHostMaxProcesses(autoTags(), 0, 253, []int64{1 << 30}, true, nil); ok {
		t.Fatal("missing memory must leave auto unresolved")
	}
}

func TestResolveAutoOneJobWhenRuntimeExceedsHost(t *testing.T) {
	next, ok := ResolveAutoHostMaxProcesses(autoTags(), 1<<30, 253, []int64{4 << 30}, true, nil)
	if !ok || next[ct.FormationMaxProcessesTag] != "1" {
		t.Fatalf("ok=%t tags=%v", ok, next)
	}
}

func TestFormatDaemonTagsArgDynamicDefault(t *testing.T) {
	got, err := formatDaemonTagsArg(nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := ct.FormationMaxProcessesTag + "=" + ct.HostMaxProcessesAuto + "," + ct.FormationMaxProcessesSourceTag + "=" + ct.HostMaxProcessesSourceAuto
	if got != want {
		t.Fatalf("new file: got %q want %q", got, want)
	}

	existingOperator := map[string]string{
		ct.FormationMaxProcessesTag:       "1",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceOperator,
		"role":                            "database",
	}
	kept, err := formatDaemonTagsArg([]string{"role=database"}, "", existingOperator)
	if err != nil {
		t.Fatal(err)
	}
	wantKept := ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator,role=database"
	if kept != wantKept {
		t.Fatalf("existing operator: got %q want %q", kept, wantKept)
	}
	capOnly, err := formatDaemonTagsArg(nil, "", existingOperator)
	if err != nil {
		t.Fatal(err)
	}
	wantCapOnly := ct.FormationMaxProcessesTag + "=1," + ct.FormationMaxProcessesSourceTag + "=operator"
	if capOnly != wantCapOnly {
		t.Fatalf("re-init without --tag keeps only the cap: got %q want %q", capOnly, wantCapOnly)
	}

	noCap, err := formatDaemonTagsArg([]string{"role=web"}, "", map[string]string{"role": "web"})
	if err != nil || noCap != "role=web" {
		t.Fatalf("existing file without a cap must stay unlimited: %q %v", noCap, err)
	}
	bare, err := formatDaemonTagsArg(nil, "", map[string]string{})
	if err != nil || bare != "" {
		t.Fatalf("existing file with no tags must stay unlimited: %q %v", bare, err)
	}

	cleared, err := formatDaemonTagsArg(nil, "unlimited", map[string]string{
		ct.FormationMaxProcessesTag:       "4",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	})
	if err != nil || cleared != "" {
		t.Fatalf("unlimited must drop cap and source: %q %v", cleared, err)
	}
}

func TestRunInitOmittedCapWritesAutoAndSecondRunKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	args := parseHostCLI(t, "init", []string{"init", "--file", path, "--tag", "role=web"})
	if err := runInit(args); err != nil {
		t.Fatal(err)
	}
	first, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(first.Args, " ")
	if !strings.Contains(joined, ct.FormationMaxProcessesTag+"="+ct.HostMaxProcessesAuto) || !strings.Contains(joined, ct.FormationMaxProcessesSourceTag+"="+ct.HostMaxProcessesSourceAuto) {
		t.Fatalf("first init args %v", first.Args)
	}
	if !strings.Contains(joined, "role=web") {
		t.Fatalf("role dropped: %v", first.Args)
	}

	again := parseHostCLI(t, "init", []string{"init", "--file", path, "--peer-ips", "10.0.0.2"})
	if err := runInit(again); err != nil {
		t.Fatal(err)
	}
	second, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(second.Args, " ")
	if !strings.Contains(text, ct.FormationMaxProcessesTag+"="+ct.HostMaxProcessesAuto) {
		t.Fatalf("second init dropped auto: %v", second.Args)
	}
	if !strings.Contains(text, "--peer-ips") {
		t.Fatalf("second init args %v", second.Args)
	}
}

func TestWriteDaemonTagsUpdatesCapAndKeepsMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.json")
	c := config.New()
	c.Args = []string{"--peer-ips", "10.0.0.1", "--tags", "role=database"}
	c.Env = map[string]string{"CONTROLLER_KEY": "secret"}
	if err := c.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	err := WriteDaemonTags(path, map[string]string{
		"role":                            "database",
		ct.FormationMaxProcessesTag:       "6",
		ct.FormationMaxProcessesSourceTag: ct.HostMaxProcessesSourceAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(got.Args, " ")
	if !strings.Contains(text, "--peer-ips") || !strings.Contains(text, "role=database") || !strings.Contains(text, ct.FormationMaxProcessesTag+"=6") {
		t.Fatalf("args %v", got.Args)
	}
	if got.Env["CONTROLLER_KEY"] != "secret" {
		t.Fatalf("env %v", got.Env)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if err := WriteDaemonTags(filepath.Join(t.TempDir(), "missing.json"), map[string]string{"role": "web"}); err != nil {
		t.Fatal(err)
	}
}
