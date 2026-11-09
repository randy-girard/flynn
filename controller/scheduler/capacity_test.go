package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/testutils"
	"github.com/randy-girard/flynn/controller/utils"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/stream"
)

// recordingStats is a host client that counts stats calls and stream follows.
// GetStatsContext is optional: leave it unset by using plainStats.
type recordingStats struct {
	*testutils.FakeHostClient
	calls    atomic.Int32
	followed atomic.Int32
	stats    *host.HostResourceStats
	err      error
	deadline atomic.Int64
	hasDL    atomic.Bool
}

func (c *recordingStats) GetStats() (*host.HostResourceStats, error) {
	c.calls.Add(1)
	if c.err != nil {
		return nil, c.err
	}
	if c.stats != nil {
		return c.stats, nil
	}
	return &host.HostResourceStats{HostID: c.ID(), MemoryTotalBytes: 1 << 30, CPUCount: 1}, nil
}

func (c *recordingStats) GetStatsContext(ctx context.Context) (*host.HostResourceStats, error) {
	if dl, ok := ctx.Deadline(); ok {
		c.hasDL.Store(true)
		c.deadline.Store(dl.UnixNano())
	}
	return c.GetStats()
}

func (c *recordingStats) StreamEvents(id string, ch chan *host.Event) (stream.Stream, error) {
	c.followed.Add(1)
	return c.FakeHostClient.StreamEvents(id, ch)
}

func (c *recordingStats) StreamVolumes(ch chan *volume.Event) (stream.Stream, error) {
	c.followed.Add(1)
	return c.FakeHostClient.StreamVolumes(ch)
}

func (c *recordingStats) ListJobs() (map[string]host.ActiveJob, error) {
	c.followed.Add(1)
	return c.FakeHostClient.ListJobs()
}

func (c *recordingStats) ListVolumes() ([]*volume.Info, error) {
	c.followed.Add(1)
	return c.FakeHostClient.ListVolumes()
}

// plainStats has no GetStatsContext, so the refresh must call GetStats.
type plainStats struct {
	*testutils.FakeHostClient
	calls atomic.Int32
}

func (c *plainStats) GetStats() (*host.HostResourceStats, error) {
	c.calls.Add(1)
	return &host.HostResourceStats{HostID: c.ID(), MemoryTotalBytes: 2 << 30, CPUCount: 2}, nil
}

func (c *plainStats) StreamEvents(id string, ch chan *host.Event) (stream.Stream, error) {
	return nil, errors.New("refresh must not follow the host")
}

func (c *plainStats) StreamVolumes(ch chan *volume.Event) (stream.Stream, error) {
	return nil, errors.New("refresh must not follow the host")
}

type blockingStats struct {
	*testutils.FakeHostClient
	calls   *atomic.Int32
	release <-chan struct{}
	started chan struct{}
	once    atomic.Bool
}

func (c *blockingStats) GetStats() (*host.HostResourceStats, error) {
	c.calls.Add(1)
	if c.once.CompareAndSwap(false, true) {
		close(c.started)
	}
	<-c.release
	return &host.HostResourceStats{HostID: c.ID(), MemoryTotalBytes: 8 << 30, CPUCount: 4}, nil
}

func (c *blockingStats) GetStatsContext(ctx context.Context) (*host.HostResourceStats, error) {
	return c.GetStats()
}

type fanoutStats struct {
	*testutils.FakeHostClient
	started *atomic.Int32
	release <-chan struct{}
}

func (c *fanoutStats) GetStats() (*host.HostResourceStats, error) {
	c.started.Add(1)
	<-c.release
	return &host.HostResourceStats{HostID: c.ID(), MemoryTotalBytes: 1 << 30, CPUCount: 1}, nil
}

func (c *fanoutStats) GetStatsContext(ctx context.Context) (*host.HostResourceStats, error) {
	return c.GetStats()
}

func capacityLoop(s *Scheduler, leader bool) {
	flag := leader
	s.isLeader = &flag
	s.loopActive = true
	s.loopApply = make(chan func(), 8)
	if s.stop == nil {
		s.stop = make(chan struct{})
	}
}

func runApplies(t *testing.T, s *Scheduler, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case fn := <-s.loopApply:
			if fn != nil {
				fn()
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for scheduler apply %d/%d", i+1, n)
		}
	}
}

func waitCalls(t *testing.T, calls *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() < n {
		t.Fatalf("GetStats calls = %d, want at least %d", calls.Load(), n)
	}
}

func TestApplyHostCapacityKeepsLastGood(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 4 << 30, CPUMilli: 4000}
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, nil)
	blocked := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(blocked)
	blocked.ID = "blocked"
	blocked.State = JobStateBlocked
	s.jobs["blocked"] = blocked
	s.hostSyncFailed = false

	s.applyHostCapacity([]capacitySample{
		{id: "missing"},
		{id: "h1", err: errors.New("timeout")},
	})
	if h.MemoryTotalBytes != 4<<30 || h.CPUMilli != 4000 {
		t.Fatalf("error sample wiped totals mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if blocked.State != JobStateBlocked {
		t.Fatalf("error sample woke job state=%s", blocked.State)
	}

	s.applyHostCapacity([]capacitySample{{id: "h1", stats: nil}})
	if h.MemoryTotalBytes != 4<<30 || h.CPUMilli != 4000 || blocked.State != JobStateBlocked {
		t.Fatal("nil stats must keep the last totals")
	}

	s.applyHostCapacity([]capacitySample{{id: "h1", stats: &host.HostResourceStats{}}})
	if h.MemoryTotalBytes != 4<<30 || h.CPUMilli != 4000 || blocked.State != JobStateBlocked {
		t.Fatal("zero sample must not wipe the last totals")
	}

	s.applyHostCapacity([]capacitySample{{id: "h1", stats: &host.HostResourceStats{CPUCount: 2}}})
	if h.MemoryTotalBytes != 4<<30 || h.CPUMilli != 2000 {
		t.Fatalf("zero memory wiped or cpu not updated mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if blocked.State != JobStateBlocked {
		t.Fatal("a smaller CPU sample must not wake blocked jobs")
	}

	s.applyHostCapacity([]capacitySample{{id: "h1", stats: &host.HostResourceStats{MemoryTotalBytes: 8 << 30}}})
	if h.MemoryTotalBytes != 8<<30 || h.CPUMilli != 2000 {
		t.Fatalf("zero cpu wiped or memory not updated mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if s.hostSyncFailed {
		t.Fatal("capacity sample must not fail the host sync")
	}
}

func TestApplyHostCapacityGrowWakesBlockedJob(t *testing.T) {
	h := NewHost(testutils.NewFakeHostClient("h1", false), log15.New())
	h.Healthy = true
	h.MemoryTotalBytes = 1 << 30
	h.CPUMilli = 1000
	running := runningReserved("small", 1<<30, 1000)
	blocked := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(blocked)
	blocked.ID = "blocked"
	blocked.State = JobStateBlocked
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, Jobs{
		"running": running,
		"blocked": blocked,
	})
	s.placementRequests = make(chan *PlacementRequest)
	done := make(chan struct{})
	go func() {
		req := <-s.placementRequests
		s.HandlePlacementRequest(req)
		close(done)
	}()

	s.applyHostCapacity([]capacitySample{{
		id:    "h1",
		stats: &host.HostResourceStats{MemoryTotalBytes: 3 << 30, CPUCount: 4},
	}})
	if h.MemoryTotalBytes != 3<<30 || h.CPUMilli != 4000 {
		t.Fatalf("mem=%d cpu=%d, want 3GiB and 4000 milliCPU", h.MemoryTotalBytes, h.CPUMilli)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("larger capacity did not start the blocked job")
	}
	if blocked.State == JobStateBlocked || blocked.HostID != "h1" {
		t.Fatalf("state=%s host=%q", blocked.State, blocked.HostID)
	}
}

func TestApplyHostCapacityShrinkDoesNotWake(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 4 << 30, CPUMilli: 4000}
	blocked := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(blocked)
	blocked.ID = "blocked"
	blocked.State = JobStateBlocked
	s := newCapacityScheduler("hosted", map[string]bool{"small": false}, h, Jobs{"blocked": blocked})
	s.placementRequests = make(chan *PlacementRequest, 1)

	s.applyHostCapacity([]capacitySample{{
		id:    "h1",
		stats: &host.HostResourceStats{MemoryTotalBytes: 2 << 30, CPUCount: 2},
	}})
	if h.MemoryTotalBytes != 2<<30 || h.CPUMilli != 2000 {
		t.Fatalf("shrink mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if blocked.State != JobStateBlocked {
		t.Fatalf("shrink woke job state=%s", blocked.State)
	}
	select {
	case <-s.placementRequests:
		t.Fatal("shrink started a placement")
	default:
	}
}

func TestApplyHostCapacityUnchangedDoesNotWake(t *testing.T) {
	h := &Host{ID: "h1", Healthy: true, MemoryTotalBytes: 2 << 30, CPUMilli: 2000}
	blocked := testJobWithProfile("small", 1<<30, 1000, 1<<30, 1000)
	markPlaceable(blocked)
	blocked.ID = "blocked"
	blocked.State = JobStateBlocked
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, Jobs{"blocked": blocked})

	s.applyHostCapacity([]capacitySample{{
		id:    "h1",
		stats: &host.HostResourceStats{MemoryTotalBytes: 2 << 30, CPUCount: 2},
	}})
	if blocked.State != JobStateBlocked {
		t.Fatal("unchanged totals must not wake blocked jobs")
	}
}

func TestBeginCapacityRefreshUsesDeadlineAndDoesNotFollow(t *testing.T) {
	if capacityRefreshTimeout != 10*time.Second {
		t.Fatalf("timeout %s, want 10s", capacityRefreshTimeout)
	}
	if capacityRefreshConcurrency != 8 {
		t.Fatalf("concurrency %d, want 8", capacityRefreshConcurrency)
	}
	client := &recordingStats{
		FakeHostClient: testutils.NewFakeHostClient("h1", false),
		stats:          &host.HostResourceStats{MemoryTotalBytes: 8 << 30, CPUCount: 4},
	}
	h := NewHost(client, log15.New())
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, nil)
	capacityLoop(s, true)
	before := time.Now()
	s.beginCapacityRefresh()
	runApplies(t, s, 1)
	if client.calls.Load() != 1 {
		t.Fatalf("GetStats calls %d", client.calls.Load())
	}
	if client.followed.Load() != 0 {
		t.Fatalf("refresh followed the host %d times", client.followed.Load())
	}
	if s.hosts["h1"] != h {
		t.Fatal("refresh replaced the host")
	}
	if h.MemoryTotalBytes != 8<<30 || h.CPUMilli != 4000 {
		t.Fatalf("mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if !client.hasDL.Load() {
		t.Fatal("GetStatsContext was not used")
	}
	got := time.Unix(0, client.deadline.Load())
	if got.Before(before.Add(capacityRefreshTimeout-time.Second)) || got.After(time.Now().Add(capacityRefreshTimeout)) {
		t.Fatalf("deadline %s", got)
	}
	if s.capacityInFlight || s.holdRectify || s.pendingLeader != nil || s.hostSyncFailed {
		t.Fatal("refresh must not stay in flight or hold the leader barrier")
	}
}

func TestBeginCapacityRefreshPlainClientUsesGetStats(t *testing.T) {
	client := &plainStats{FakeHostClient: testutils.NewFakeHostClient("h1", false)}
	h := NewHost(client, log15.New())
	s := newCapacityScheduler("hosted", map[string]bool{"small": false}, h, nil)
	capacityLoop(s, true)
	s.beginCapacityRefresh()
	runApplies(t, s, 1)
	if client.calls.Load() != 1 {
		t.Fatalf("GetStats calls %d", client.calls.Load())
	}
	if h.MemoryTotalBytes != 2<<30 || h.CPUMilli != 2000 {
		t.Fatalf("mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
}

func TestBeginCapacityRefreshSkipsFollowerAndUnplaceableHosts(t *testing.T) {
	client := &recordingStats{FakeHostClient: testutils.NewFakeHostClient("h1", false)}
	h := NewHost(client, log15.New())
	s := newCapacityScheduler("self_hosted", nil, h, nil)
	capacityLoop(s, false)
	s.beginCapacityRefresh()
	select {
	case fn := <-s.loopApply:
		if fn != nil {
			fn()
		}
		t.Fatal("follower started a capacity refresh")
	case <-time.After(200 * time.Millisecond):
	}
	if client.calls.Load() != 0 {
		t.Fatalf("follower GetStats calls %d", client.calls.Load())
	}

	leader := true
	s.isLeader = &leader
	h.Shutdown = true
	s.beginCapacityRefresh()
	h.Shutdown = false
	h.Healthy = false
	s.beginCapacityRefresh()
	h.Healthy = true
	h.client = nil
	s.beginCapacityRefresh()
	if client.calls.Load() != 0 || s.capacityInFlight {
		t.Fatalf("unplaceable host calls=%d inFlight=%v", client.calls.Load(), s.capacityInFlight)
	}
}

func TestBeginCapacityRefreshCoalesces(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	client := &blockingStats{
		FakeHostClient: testutils.NewFakeHostClient("h1", false),
		calls:          &calls,
		release:        release,
		started:        make(chan struct{}),
	}
	h := NewHost(client, log15.New())
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, nil)
	capacityLoop(s, true)
	s.beginCapacityRefresh()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("first refresh did not call GetStats")
	}
	s.beginCapacityRefresh()
	if !s.capacityInFlight || !s.capacityAgain {
		t.Fatalf("inFlight=%v again=%v", s.capacityInFlight, s.capacityAgain)
	}
	if calls.Load() != 1 {
		t.Fatalf("overlapping refresh calls %d", calls.Load())
	}
	close(release)
	runApplies(t, s, 1)
	runApplies(t, s, 1)
	if calls.Load() != 2 {
		t.Fatalf("coalesced calls %d, want 2", calls.Load())
	}
	if s.capacityInFlight || s.capacityAgain {
		t.Fatal("refresh still in flight after the coalesced round")
	}
	if h.CPUMilli != 4000 {
		t.Fatalf("cpu=%d", h.CPUMilli)
	}
}

func TestBeginCapacityRefreshLimitsConcurrency(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int32
	s := &Scheduler{
		logger: log15.New(),
		jobs:   Jobs{},
		hosts:  map[string]*Host{},
		stop:   make(chan struct{}),
	}
	leader := true
	s.isLeader = &leader
	s.loopActive = true
	s.loopApply = make(chan func(), 4)
	for i := 0; i < 9; i++ {
		id := string(rune('a' + i))
		client := &fanoutStats{
			FakeHostClient: testutils.NewFakeHostClient(id, false),
			started:        &started,
			release:        release,
		}
		h := NewHost(client, s.logger)
		s.hosts[h.ID] = h
	}
	s.beginCapacityRefresh()
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if started.Load() != 8 {
		t.Fatalf("started %d, want 8 before the rest", started.Load())
	}
	time.Sleep(100 * time.Millisecond)
	if started.Load() != 8 {
		t.Fatalf("concurrency %d, want 8", started.Load())
	}
	close(release)
	runApplies(t, s, 1)
	if started.Load() != 9 {
		t.Fatalf("started %d, want 9", started.Load())
	}
}

func TestStartupSyncDoesNotRefreshCapacity(t *testing.T) {
	client := &recordingStats{FakeHostClient: testutils.NewFakeHostClient("h1", false)}
	h := NewHost(client, log15.New())
	h.MemoryTotalBytes = 2 << 30
	h.CPUMilli = 2000
	s := newCapacityScheduler("self_hosted", nil, h, nil)
	s.loopActive = false
	s.ClusterClient = testutils.NewFakeClusterWithHosts(map[string]utils.HostClient{h.ID: client})
	s.beginAsyncSyncHosts()
	if client.calls.Load() != 0 {
		t.Fatalf("startup sync GetStats calls %d", client.calls.Load())
	}
	if h.MemoryTotalBytes != 2<<30 || h.CPUMilli != 2000 {
		t.Fatal("startup sync changed totals")
	}
}

func TestBeginAsyncSyncHostsRefreshesOnlyTheLeader(t *testing.T) {
	client := &recordingStats{
		FakeHostClient: testutils.NewFakeHostClient("h1", false),
		stats:          &host.HostResourceStats{MemoryTotalBytes: 6 << 30, CPUCount: 3},
	}
	h := NewHost(client, log15.New())
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, nil)
	s.ClusterClient = testutils.NewFakeClusterWithHosts(map[string]utils.HostClient{h.ID: client})
	capacityLoop(s, false)
	s.beginAsyncSyncHosts()
	runApplies(t, s, 1)
	time.Sleep(200 * time.Millisecond)
	if client.calls.Load() != 0 {
		t.Fatalf("follower host sync GetStats calls %d", client.calls.Load())
	}
	if s.hostsInFlight {
		t.Fatal("follower host sync still in flight")
	}

	leader := true
	s.isLeader = &leader
	s.beginAsyncSyncHosts()
	runApplies(t, s, 2)
	if client.calls.Load() != 1 || client.followed.Load() != 0 {
		t.Fatalf("calls=%d followed=%d", client.calls.Load(), client.followed.Load())
	}
	if h.MemoryTotalBytes != 6<<30 || h.CPUMilli != 3000 {
		t.Fatalf("mem=%d cpu=%d", h.MemoryTotalBytes, h.CPUMilli)
	}
	if s.hostSyncFailed || s.holdRectify || s.pendingLeader != nil || s.capacityInFlight || s.hostsInFlight {
		t.Fatal("leader refresh failed the host sync or held rectify")
	}
}

func TestCapacityRefreshErrorDoesNotFailHostSync(t *testing.T) {
	client := &recordingStats{
		FakeHostClient: testutils.NewFakeHostClient("h1", false),
		err:            errors.New("blackhole"),
	}
	h := NewHost(client, log15.New())
	h.MemoryTotalBytes = 5 << 30
	h.CPUMilli = 5000
	s := newCapacityScheduler("hosted", map[string]bool{"small": false}, h, nil)
	s.ClusterClient = testutils.NewFakeClusterWithHosts(map[string]utils.HostClient{h.ID: client})
	capacityLoop(s, true)
	s.beginAsyncSyncHosts()
	runApplies(t, s, 2)
	if client.calls.Load() != 1 {
		t.Fatalf("calls %d", client.calls.Load())
	}
	if h.MemoryTotalBytes != 5<<30 || h.CPUMilli != 5000 {
		t.Fatal("failed refresh wiped totals")
	}
	if s.hostSyncFailed || s.hostsInFlight || s.capacityInFlight {
		t.Fatal("stats error failed the host sync")
	}
}

func TestApplyHostChecksStartsCapacityRefresh(t *testing.T) {
	client := &recordingStats{
		FakeHostClient: testutils.NewFakeHostClient("h1", false),
		stats:          &host.HostResourceStats{MemoryTotalBytes: 3 << 30, CPUCount: 2},
	}
	h := NewHost(client, log15.New())
	h.Healthy = false
	s := newCapacityScheduler("self_hosted", map[string]bool{"small": true}, h, nil)
	capacityLoop(s, true)
	s.applyHostChecks([]hostCheckResult{{id: "h1"}})
	if !h.Healthy {
		t.Fatal("host was not marked healthy")
	}
	runApplies(t, s, 1)
	if client.calls.Load() != 1 || h.CPUMilli != 2000 || h.MemoryTotalBytes != 3<<30 {
		t.Fatalf("calls=%d mem=%d cpu=%d", client.calls.Load(), h.MemoryTotalBytes, h.CPUMilli)
	}

	client.calls.Store(0)
	follower := false
	s.isLeader = &follower
	h.Healthy = false
	h.MemoryTotalBytes = 1
	h.CPUMilli = 1
	s.applyHostChecks([]hostCheckResult{{id: "h1"}})
	time.Sleep(200 * time.Millisecond)
	if !h.Healthy || client.calls.Load() != 0 || h.MemoryTotalBytes != 1 || h.CPUMilli != 1 {
		t.Fatalf("follower calls=%d mem=%d cpu=%d healthy=%v", client.calls.Load(), h.MemoryTotalBytes, h.CPUMilli, h.Healthy)
	}
}
