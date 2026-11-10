package buildadmit

import (
	"errors"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
)

func limits() Limits { return Limits{Cluster: 8, Account: 3} }

func TestStoredLimitsRejectsUnlimitedAndCorrupt(t *testing.T) {
	def := StoredLimits(0, 0)
	if def.Cluster != DefaultClusterLimit || def.Account != DefaultAccountLimit {
		t.Fatalf("zero = %+v, want %d/%d", def, DefaultClusterLimit, DefaultAccountLimit)
	}
	for _, in := range []Limits{
		{Cluster: 8, Account: 8},
		{Cluster: 3, Account: 8},
		{Cluster: 1, Account: 1},
		{Cluster: -1, Account: 3},
		{Cluster: 8, Account: 0},
	} {
		got := StoredLimits(in.Cluster, in.Account)
		if got != def {
			t.Fatalf("StoredLimits(%d, %d) = %+v, want defaults", in.Cluster, in.Account, got)
		}
		if err := ValidLimits(in); err == nil {
			t.Fatalf("ValidLimits(%+v) accepted an illegal pair", in)
		}
	}
	ok := Limits{Cluster: 4, Account: 1}
	if got := StoredLimits(ok.Cluster, ok.Account); got != ok {
		t.Fatalf("valid stored = %+v", got)
	}
	if err := ValidLimits(ok); err != nil {
		t.Fatal(err)
	}
}

func TestCanCreateCountsInFlightBuildersByOwner(t *testing.T) {
	jobs := []Job{
		{ID: "a1", Type: "slugbuilder", Owner: "user:a", State: StatePending},
		{ID: "a2", Type: "dockerbuilder-24", Owner: " user:a ", State: StateUp},
		{ID: "a3", Type: "slugbuilder", Owner: "user:a", State: StateStarting},
		{ID: "down", Type: "slugbuilder", Owner: "user:a", State: StateDown},
		{ID: "stop", Type: "slugbuilder", Owner: "user:a", State: StateStopping},
		{ID: "web", Type: "web", Owner: "user:a", State: StateUp},
		{ID: "run", Type: "slugrunner", Owner: "user:a", State: StateUp},
		{ID: "b1", Type: "dockerbuilder", Owner: "user:b", State: StateBlocked},
	}
	err := CanCreate(jobs, "user:a", limits())
	var capErr CapacityError
	if !errors.As(err, &capErr) || capErr.Kind != LimitAccount {
		t.Fatalf("account at 3 = %v, want account cap", err)
	}
	if err := CanCreate(jobs, "user:b", limits()); err != nil {
		t.Fatalf("other account under cap: %v", err)
	}
	// cluster:admin is not a parameter and is not exempt.
	if err := CanCreate(jobs, "user:c", limits()); err != nil {
		t.Fatalf("fresh account: %v", err)
	}
}

func TestCanCreateEmptyOwnerIsOneBucket(t *testing.T) {
	jobs := []Job{
		{ID: "1", Type: "slugbuilder", Owner: "", State: StatePending},
		{ID: "2", Type: "slugbuilder", Owner: "  ", State: StateUp},
		{ID: "3", Type: "dockerbuilder", Owner: "", State: StateStarting},
	}
	if err := CanCreate(jobs, "", limits()); err == nil {
		t.Fatal("empty owner must share one account bucket")
	}
	if err := CanCreate(jobs, "   ", limits()); err == nil {
		t.Fatal("whitespace owner must share the empty bucket")
	}
	if err := CanCreate(jobs, "user:a", limits()); err != nil {
		t.Fatalf("named owner is a different bucket: %v", err)
	}
}

func TestCanCreateClusterFullBeforeAccount(t *testing.T) {
	var jobs []Job
	for i := 0; i < 8; i++ {
		jobs = append(jobs, Job{
			ID:    string(rune('a' + i)),
			Type:  "slugbuilder",
			Owner: "user:" + string(rune('a'+i)),
			State: StatePending,
		})
	}
	err := CanCreate(jobs, "user:z", Limits{Cluster: 8, Account: 3})
	var capErr CapacityError
	if !errors.As(err, &capErr) || capErr.Kind != LimitCluster {
		t.Fatalf("cluster full = %v, want cluster cap", err)
	}
	if !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("message %q must name the cluster cap", err.Error())
	}
}

func TestSelectPendingKeepsAccountUnderCap(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	jobs := []Job{
		{ID: "a-run-1", Type: "slugbuilder", Owner: "user:a", State: StateUp, CreatedAt: now},
		{ID: "a-run-2", Type: "slugbuilder", Owner: "user:a", State: StateStarting, CreatedAt: now},
		{ID: "a-run-3", Type: "slugbuilder", Owner: "user:a", State: StateUp, CreatedAt: now},
		{ID: "a-wait", Type: "slugbuilder", Owner: "user:a", State: StatePending, CreatedAt: now.Add(time.Second)},
		{ID: "b-wait", Type: "dockerbuilder", Owner: "user:b", State: StatePending, CreatedAt: now.Add(2 * time.Second)},
		{ID: "c-old", Type: "slugbuilder", Owner: "user:c", State: StatePending, CreatedAt: now.Add(-time.Minute)},
	}
	got := ids(SelectPending(jobs, limits()))
	if contains(got, "a-wait") {
		t.Fatal("account at its cap must not take the next slot")
	}
	if !contains(got, "c-old") || !contains(got, "b-wait") {
		t.Fatalf("other accounts should start, got %v", got)
	}
}

func TestSelectPendingDropsOverCapNewest(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	jobs := []Job{
		{ID: "new", Type: "slugbuilder", Owner: "user:a", State: StatePending, CreatedAt: now.Add(time.Minute)},
		{ID: "old", Type: "slugbuilder", Owner: "user:a", State: StatePending, CreatedAt: now},
	}
	got := ids(SelectPending(jobs, Limits{Cluster: 2, Account: 1}))
	if len(got) != 1 || got[0] != "old" {
		t.Fatalf("over-cap race starts %v, want only old", got)
	}
}

func TestHostJobOmitsReleaseEnvAndTrust(t *testing.T) {
	job := HostJob(HostJobInput{
		HostID:     "host1",
		UUID:       "uuid-1",
		AppID:      "app",
		AppName:    "shop",
		ReleaseID:  "rel",
		Type:       "slugbuilder",
		Owner:      " user:a ",
		SystemApp:  true,
		Args:       []string{"/builder/build.sh"},
		Env:        map[string]string{"CONTROLLER_KEY": "secret", "SSH_CLIENT_KEY": "ssh", "STACK": "cflinuxfs4"},
		ReleaseEnv: map[string]string{"DATABASE_URL": "postgres://db", "CONTROLLER_KEY": "secret"},
		Token:      "tok",
		Resources:  resource.Defaults(),
	})
	if job.ID != "host1-uuid-1" {
		t.Fatalf("id = %q", job.ID)
	}
	if job.Partition != string(ct.PartitionTypeBackground) {
		t.Fatalf("partition = %q", job.Partition)
	}
	if job.Metadata["flynn-system-app"] != "" || job.Metadata["flynn-controller.formation"] != "" {
		t.Fatalf("meta leaked trust: %v", job.Metadata)
	}
	if job.Metadata["owner_account"] != "user:a" {
		t.Fatalf("owner = %q", job.Metadata["owner_account"])
	}
	if job.Metadata["flynn-controller.type"] != "slugbuilder" {
		t.Fatalf("type meta = %q", job.Metadata["flynn-controller.type"])
	}
	if _, ok := job.Config.Env["CONTROLLER_KEY"]; ok {
		t.Fatal("CONTROLLER_KEY must not be in the job env")
	}
	if job.Config.Env["DATABASE_URL"] != "" {
		t.Fatal("release env must not be copied onto the compile")
	}
	if job.Config.Env["SSH_CLIENT_KEY"] != "ssh" || job.Config.Env["STACK"] != "cflinuxfs4" {
		t.Fatalf("build env = %v", job.Config.Env)
	}
	if !job.Config.Stdin {
		t.Fatal("compile must read stdin")
	}
	if len(job.Config.Secrets) != 1 || job.Config.Secrets[0].Path != TokenPath || string(job.Config.Secrets[0].Data) != "tok" {
		t.Fatalf("secrets = %#v", job.Config.Secrets)
	}
	if job.Resurrect {
		t.Fatal("a build must not resurrect")
	}

	bare := HostJob(HostJobInput{HostID: "h", UUID: "u", Type: "dockerbuilder", Env: map[string]string{"A": "b"}})
	if len(bare.Config.Secrets) != 0 {
		t.Fatalf("empty token must not mount a secret: %#v", bare.Config.Secrets)
	}
}

func TestForeignCompileAndEphemeralVolume(t *testing.T) {
	if !ForeignCompile("slugbuilder", "hostA-uuid", "hostB") {
		t.Fatal("compile job id prefix must match the host that receives AddJob")
	}
	if ForeignCompile("slugbuilder", "hostB-uuid", "hostB") {
		t.Fatal("matching prefix is this host's compile")
	}
	if ForeignCompile("web", "hostA-uuid", "hostB") {
		t.Fatal("non-compile jobs are not rejected for a foreign prefix")
	}
	if ForeignCompile("slugbuilder-24", "not-a-job", "hostB") {
		// invalid id has no host prefix; a compile must still be rejected
	} else {
		t.Fatal("compile without a host prefix is foreign")
	}
	if !JobOnHost("hostB-uuid", "hostB") || JobOnHost("hostA-uuid", "hostB") {
		t.Fatal("JobOnHost")
	}

	vols := []VolumeRef{
		{ID: "vol-a", HostID: "hostA", Path: "/tmp", DeleteOnStop: true},
		{ID: "data", HostID: "hostA", Path: "/data", DeleteOnStop: false},
		{ID: "fresh", HostID: "", Path: "/tmp", DeleteOnStop: true},
	}
	kept := KeepVolumes(vols, "")
	if len(kept) != 2 {
		t.Fatalf("cleared placement kept %#v", kept)
	}
	for _, v := range kept {
		if v.ID == "vol-a" {
			t.Fatal("ephemeral volume bound to another host must be dropped when HostID is cleared")
		}
	}
}

func TestWaitForPlacement(t *testing.T) {
	if GitPushWaits != true {
		t.Fatal("an accepted git push waits for placement and the compile")
	}
	if MaxPendingWait != 30*time.Minute {
		t.Fatalf("pending wait = %s", MaxPendingWait)
	}
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	now := start
	calls := 0
	get := func() (PlacementJob, error) {
		calls++
		if calls < 3 {
			return PlacementJob{ID: "hostB-uuid", HostID: "hostB", State: StatePending, Type: "slugbuilder"}, nil
		}
		return PlacementJob{ID: "hostB-uuid", HostID: "hostB", State: StateStarting, Type: "slugbuilder"}, nil
	}
	got, err := WaitForPlacement(get, start.Add(time.Second), func() time.Time { return now }, func(d time.Duration) { now = now.Add(d) })
	if err != nil || got.State != StateStarting {
		t.Fatalf("ready = %+v %v", got, err)
	}

	_, err = WaitForPlacement(func() (PlacementJob, error) {
		return PlacementJob{State: StateDown, Type: "slugbuilder"}, nil
	}, start.Add(time.Second), func() time.Time { return start }, func(time.Duration) {})
	if !errors.Is(err, ErrPlacementStopped) {
		t.Fatalf("down = %v", err)
	}

	_, err = WaitForPlacement(func() (PlacementJob, error) {
		return PlacementJob{ID: "hostA-uuid", HostID: "hostB", State: StateStarting, Type: "dockerbuilder"}, nil
	}, start.Add(time.Second), func() time.Time { return start }, func(time.Duration) {})
	if !errors.Is(err, ErrPlacementHost) {
		t.Fatalf("wrong host = %v", err)
	}

	_, err = WaitForPlacement(func() (PlacementJob, error) {
		return PlacementJob{State: StatePending, Type: "slugbuilder"}, nil
	}, start, func() time.Time { return start }, func(time.Duration) {})
	if !errors.Is(err, ErrPlacementTimeout) {
		t.Fatalf("deadline = %v", err)
	}
}

func ids(jobs []Job) []string {
	out := make([]string, len(jobs))
	for i, j := range jobs {
		out[i] = j.ID
	}
	return out
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
