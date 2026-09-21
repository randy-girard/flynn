package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
	"golang.org/x/net/context"
)

const testAppID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func metaJob(id, appID, typ string) host.ActiveJob {
	return host.ActiveJob{Job: &host.Job{
		ID: id,
		Metadata: map[string]string{
			"flynn-controller.app":     appID,
			"flynn-controller.release": "rel-1",
			"flynn-controller.type":    typ,
		},
	}}
}

func TestJobBelongsToAppMetadataAndFallback(t *testing.T) {
	owned := metaJob("job-1", testAppID, "web")
	if !jobBelongsToApp(owned, "job-1", testAppID) {
		t.Fatal("metadata match")
	}
	other := metaJob("job-2", "other-app", "web")
	if jobBelongsToApp(other, "job-2", testAppID) {
		t.Fatal("other app must not match")
	}
	fallback := host.ActiveJob{Job: &host.Job{ID: "host-x-" + testAppID}}
	if !jobBelongsToApp(fallback, fallback.Job.ID, testAppID) {
		t.Fatal("job ID substring fallback")
	}
	if jobBelongsToApp(fallback, fallback.Job.ID, "nope") {
		t.Fatal("unrelated ID")
	}
}

func TestFilterAppContainerStatsIndexesByJobID(t *testing.T) {
	otherApp := "ffffffff-0000-1111-2222-333333333333"
	fallbackID := "host-x-" + testAppID
	jobs := map[string]host.ActiveJob{
		"job-app":      metaJob("job-app", testAppID, "web"),
		"job-other":    metaJob("job-other", otherApp, "web"),
		"job-internal": metaJob("job-internal", testAppID, "slugbuilder"),
		fallbackID:     {Job: &host.Job{ID: fallbackID}},
	}
	stats := []*host.ContainerStats{
		{JobID: "job-app"},
		{JobID: "job-other"},
		{JobID: "job-internal"},
		{JobID: fallbackID},
		{JobID: "missing"},
	}

	got := filterAppContainerStats(jobs, stats, testAppID, true)
	ids := map[string]bool{}
	for _, s := range got {
		ids[s.JobID] = true
	}
	if len(got) != 2 || !ids["job-app"] || !ids[fallbackID] {
		t.Fatalf("hide-internal filter = %v", ids)
	}
	if ids["job-internal"] || ids["job-other"] || ids["missing"] {
		t.Fatalf("leaked jobs = %v", ids)
	}

	shown := filterAppContainerStats(jobs, stats, testAppID, false)
	shownIDs := map[string]bool{}
	for _, s := range shown {
		shownIDs[s.JobID] = true
	}
	if !shownIDs["job-internal"] {
		t.Fatal("internal jobs must remain when not hiding")
	}
}

func TestEnrichAppJobStatsIndexesByJobID(t *testing.T) {
	fallbackID := "host-x-" + testAppID
	jobs := map[string]host.ActiveJob{
		"job-app":      metaJob("job-app", testAppID, "web"),
		"job-internal": metaJob("job-internal", testAppID, "slugbuilder"),
		fallbackID:     {Job: &host.Job{ID: fallbackID}},
	}
	stats := []*host.ContainerStats{
		{JobID: "job-app"},
		{JobID: "job-internal"},
		{JobID: fallbackID},
		{JobID: "missing"},
	}
	got := enrichAppJobStats(jobs, stats, testAppID, true)
	if len(got) != 1 || got[0].JobID != "job-app" || got[0].ProcessType != "web" || got[0].ReleaseID != "rel-1" {
		t.Fatalf("%+v", got)
	}
}

func TestEnrichClusterJobStatsIndexesByJobID(t *testing.T) {
	jobs := map[string]host.ActiveJob{
		"job-app": metaJob("job-app", testAppID, "web"),
	}
	got := enrichClusterJobStats("host-1", jobs, []*host.ContainerStats{
		{JobID: "job-app"},
		{JobID: "unknown"},
	})
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].HostID != "host-1" || got[0].AppID != testAppID || got[0].ProcessType != "web" {
		t.Fatalf("%+v", got[0])
	}
	if got[1].AppID != "" {
		t.Fatalf("unknown job should have empty metadata: %+v", got[1])
	}
}

type countingStatsHost struct {
	id        string
	jobs      map[string]host.ActiveJob
	allStats  *host.AllJobsStats
	hostStats *host.HostResourceStats
	listN     int32
	allN      int32
	statsN    int32
	listErr   error
	allErr    error
	statsErr  error
	delay     time.Duration
}

func (h *countingStatsHost) ID() string { return h.id }

func (h *countingStatsHost) wait(ctx context.Context) error {
	if h.delay <= 0 {
		return nil
	}
	select {
	case <-time.After(h.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *countingStatsHost) ListJobs() (map[string]host.ActiveJob, error) {
	return h.ListJobsContext(context.Background())
}

func (h *countingStatsHost) ListJobsContext(ctx context.Context) (map[string]host.ActiveJob, error) {
	atomic.AddInt32(&h.listN, 1)
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	if h.listErr != nil {
		return nil, h.listErr
	}
	return h.jobs, nil
}

func (h *countingStatsHost) GetAllJobsStats() (*host.AllJobsStats, error) {
	return h.GetAllJobsStatsContext(context.Background())
}

func (h *countingStatsHost) GetAllJobsStatsContext(ctx context.Context) (*host.AllJobsStats, error) {
	atomic.AddInt32(&h.allN, 1)
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	if h.allErr != nil {
		return nil, h.allErr
	}
	return h.allStats, nil
}

func (h *countingStatsHost) GetStats() (*host.HostResourceStats, error) {
	return h.GetStatsContext(context.Background())
}

func (h *countingStatsHost) GetStatsContext(ctx context.Context) (*host.HostResourceStats, error) {
	atomic.AddInt32(&h.statsN, 1)
	if err := h.wait(ctx); err != nil {
		return nil, err
	}
	if h.statsErr != nil {
		return nil, h.statsErr
	}
	return h.hostStats, nil
}

func hostWithJobs(id string, jobs ...host.ActiveJob) *countingStatsHost {
	indexed := make(map[string]host.ActiveJob, len(jobs))
	stats := make([]*host.ContainerStats, 0, len(jobs))
	for _, j := range jobs {
		indexed[j.Job.ID] = j
		stats = append(stats, &host.ContainerStats{JobID: j.Job.ID})
	}
	return &countingStatsHost{
		id:        id,
		jobs:      indexed,
		allStats:  &host.AllJobsStats{HostID: id, Jobs: stats},
		hostStats: &host.HostResourceStats{HostID: id},
	}
}

func TestCollectAppJobsStatsListsJobsOncePerHost(t *testing.T) {
	h1 := hostWithJobs("h1",
		metaJob("j1", testAppID, "web"),
		metaJob("j2", testAppID, "worker"),
		metaJob("j3", "other", "web"),
	)
	h2 := hostWithJobs("h2",
		metaJob("j4", testAppID, "web"),
		metaJob("j5", testAppID, "slugbuilder"),
	)
	got := collectAppJobsStats(context.Background(), []statsHost{h1, h2}, testAppID, true)
	if atomic.LoadInt32(&h1.listN) != 1 || atomic.LoadInt32(&h1.allN) != 1 {
		t.Fatalf("h1 ListJobs=%d GetAllJobsStats=%d (want 1 each)", h1.listN, h1.allN)
	}
	if atomic.LoadInt32(&h2.listN) != 1 || atomic.LoadInt32(&h2.allN) != 1 {
		t.Fatalf("h2 ListJobs=%d GetAllJobsStats=%d (want 1 each)", h2.listN, h2.allN)
	}
	if len(got) != 3 {
		t.Fatalf("got %d jobs, want 3 (app jobs minus slugbuilder)", len(got))
	}

	enriched := collectAppJobsStatsEnriched(context.Background(), []statsHost{h1, h2}, testAppID, true)
	if atomic.LoadInt32(&h1.listN) != 2 || atomic.LoadInt32(&h2.listN) != 2 {
		t.Fatalf("second collect must still be one ListJobs per host: h1=%d h2=%d", h1.listN, h2.listN)
	}
	if len(enriched) != 3 {
		t.Fatalf("enriched=%d", len(enriched))
	}
}

func TestCollectAppJobsStatsHostTimeout(t *testing.T) {
	prev := statsHostTimeout
	statsHostTimeout = 30 * time.Millisecond
	defer func() { statsHostTimeout = prev }()

	slow := hostWithJobs("slow", metaJob("slow-job", testAppID, "web"))
	slow.delay = time.Second
	fast := hostWithJobs("fast", metaJob("fast-job", testAppID, "web"))

	start := time.Now()
	got := collectAppJobsStats(context.Background(), []statsHost{slow, fast}, testAppID, false)
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Fatalf("stuck host blocked collector for %s", elapsed)
	}
	if len(got) != 1 || got[0].JobID != "fast-job" {
		t.Fatalf("want only fast-job, got %+v", got)
	}
}

func TestCollectClusterStatsSkipsFailedHost(t *testing.T) {
	ok := hostWithJobs("ok")
	bad := hostWithJobs("bad")
	bad.statsErr = errors.New("wedged")
	got := collectClusterStats(context.Background(), []statsHost{ok, bad})
	if len(got) != 1 || got[0].HostID != "ok" {
		t.Fatalf("%+v", got)
	}
}
