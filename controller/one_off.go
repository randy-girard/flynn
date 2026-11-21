package main

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/randy-girard/flynn/controller/placement"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/controller/utils"
	"github.com/randy-girard/flynn/host/resource"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

// oneOffReservationTTL is how long an admitted one-off counts from memory
// alone. The scheduler normally writes the job_cache row within a second of
// AddJob. After this the job counts only once that row exists.
const oneOffReservationTTL = 2 * time.Minute

type oneOffReservation struct {
	account string
	mb      int64
	expires time.Time
}

// oneOffLedger closes the gap between AddJob and the scheduler's job_cache
// write, so back-to-back one-offs cannot each pass the quota alone. mu also
// serializes the quota check and the reservation in this process. Another
// controller web process has its own ledger.
type oneOffLedger struct {
	mu   sync.Mutex
	byID map[string]oneOffReservation
}

var oneOffs = &oneOffLedger{byID: make(map[string]oneOffReservation)}

// oneOffUsageLocked is the account's running one-offs: job_cache rows that
// carry the one-off stamp, plus reservations whose job has no row yet.
// A reservation whose job has any row (even down) is dropped. Callers hold mu.
func (c *controllerAPI) oneOffUsageLocked(account string, now time.Time) (int, int64, error) {
	if c == nil || c.tenancy == nil || account == "" {
		return 0, 0, nil
	}
	rows, err := c.tenancy.ActiveOneOffs(account)
	if err != nil {
		return 0, 0, err
	}
	seen := make(map[string]bool, len(rows))
	n := 0
	var mb int64
	for _, r := range rows {
		seen[r.UUID] = true
		n++
		if mb, err = addMemoryMB(mb, r.MemoryMB); err != nil {
			return 0, 0, err
		}
	}
	var pending []string
	for id, r := range oneOffs.byID {
		if now.After(r.expires) {
			delete(oneOffs.byID, id)
			continue
		}
		if r.account != account {
			continue
		}
		if seen[id] {
			delete(oneOffs.byID, id)
			continue
		}
		pending = append(pending, id)
	}
	known, err := c.tenancy.KnownJobs(pending)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range pending {
		if known[id] {
			delete(oneOffs.byID, id)
			continue
		}
		n++
		if mb, err = addMemoryMB(mb, oneOffs.byID[id].mb); err != nil {
			return 0, 0, err
		}
	}
	return n, mb, nil
}

// oneOffUsage is oneOffUsageLocked for callers outside admitOneOff
// (formation scale).
func (c *controllerAPI) oneOffUsage(account string) (int, int64, error) {
	oneOffs.mu.Lock()
	defer oneOffs.mu.Unlock()
	return c.oneOffUsageLocked(account, time.Now())
}

// admitOneOff checks a one-off of an owned app against the account's
// max_processes and max_memory_mb, counting formations and running
// one-offs, then reserves it under jobUUID and stamps job meta with the
// admitted MiB. The returned cancel drops the reservation when AddJob
// fails. Apps without an owner are not checked or stamped.
func (c *controllerAPI) admitOneOff(app *ct.App, procType, jobUUID string, resources resource.Resources, meta map[string]string) (func(), error) {
	noop := func() {}
	if !scaleQuotaApplies(c, app) {
		return noop, nil
	}
	extra, err := oneOffMemoryMB(app.Name, procType, resources, c.buildJobMemoryAvailable())
	if err != nil {
		return noop, err
	}
	forms, err := c.accountFormations(app.OwnerAccount)
	if err != nil {
		return noop, err
	}
	hosts, err := c.memoryHosts(forms)
	if err != nil {
		return noop, err
	}
	profiles, err := c.memoryProfiles(forms)
	if err != nil {
		return noop, err
	}
	formMem, err := accountMemoryMB(app.OwnerAccount, forms, hosts, profiles)
	if err != nil {
		return noop, err
	}
	usage, err := c.tenancy.Usage(app.OwnerAccount)
	if err != nil {
		return noop, err
	}

	oneOffs.mu.Lock()
	defer oneOffs.mu.Unlock()
	now := time.Now()
	running, runningMem, err := c.oneOffUsageLocked(app.OwnerAccount, now)
	if err != nil {
		return noop, err
	}
	total, err := addMemoryMB(formMem, runningMem)
	if err != nil {
		return noop, err
	}
	if total, err = addMemoryMB(total, extra); err != nil {
		return noop, err
	}
	memArg, err := memoryQuotaArg(total)
	if err != nil {
		return noop, err
	}
	if err := c.quotaAllows(app.OwnerAccount, 0, usage.Processes+running+1, memArg, 0, 0); err != nil {
		return noop, err
	}
	oneOffs.byID[jobUUID] = oneOffReservation{account: app.OwnerAccount, mb: extra, expires: now.Add(oneOffReservationTTL)}
	if meta != nil {
		meta[ct.JobMetaOneOffMemoryMB] = strconv.FormatInt(extra, 10)
	}
	return func() {
		oneOffs.mu.Lock()
		delete(oneOffs.byID, jobUUID)
		oneOffs.mu.Unlock()
	}, nil
}

// pickOneOffHost chooses a random host for a one-off. Hosts that are
// shutting down are skipped. While placement is split, the host must also
// pass placement.HostAllows for the app's role, the same filter the
// scheduler uses for formation jobs, so a user one-off never lands on a
// flynn-role=core host.
func (c *controllerAPI) pickOneOffHost(app *ct.App) (utils.HostClient, error) {
	hosts, err := c.clusterClient.Hosts()
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("no hosts found")
	}
	mode, err := c.workloadPlacement()
	if err != nil {
		return nil, err
	}
	class := placement.Classify(app, false)
	allowed := make([]utils.HostClient, 0, len(hosts))
	for _, h := range hosts {
		if h == nil || placementHost(h).Shutdown {
			continue
		}
		if !placement.HostAllows(mode, class, h.Tags()) {
			continue
		}
		allowed = append(allowed, h)
	}
	if len(allowed) == 0 {
		msg := "no host that is not shutting down can run this job"
		if mode == placement.ModeSplit {
			msg = fmt.Sprintf("no host that is not shutting down has %s=%s, which placement split requires for this job", placement.TagRole, class.Role)
		}
		return nil, httphelper.JSONError{Code: httphelper.ServiceUnavailableErrorCode, Message: msg}
	}
	return allowed[random.Math.Intn(len(allowed))], nil
}
