// Package productioncheck is the rows of flynn-host production:check. The
// host CLI prints them as a table and adds the two rows that read the host it
// runs on (zfs, overlay). The controller runs the same rows, without those
// two, before PUT /tenancy switches a cluster to hosted, so a direct PUT
// cannot skip the check (NEW-45).
package productioncheck

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// Row is one line of the check: status ok, warn or fail.
type Row struct {
	Status string
	Check  string
	Detail string
	Action string
}

// APIError is an API whose every call returns Err.
type APIError struct {
	Err error
}

func (e APIError) Get(string, interface{}) error                      { return e.Err }
func (e APIError) GetAppRelease(string) (*ct.Release, error)          { return nil, e.Err }
func (e APIError) ListRuntimeProfiles() ([]*ct.RuntimeProfile, error) { return nil, e.Err }

// Config is the data the rows read. Tests stub every field.
type Config struct {
	API   API
	Hosts func() ([]*cluster.Host, error)
	// Now is the clock for the backup and service-token lines. Nil uses
	// time.Now.
	Now func() time.Time
	// AssumeMode, when self_hosted or hosted, is the mode used by every
	// line after a successful tenancy read. production:check leaves it
	// empty. A switch to hosted sets hosted so warnings that would become
	// failures after the switch are visible before the switch. A tenancy
	// read error is still a failure and is not replaced by AssumeMode.
	AssumeMode string
	// Jobs samples running jobs for the placement line. Nil means there is
	// nothing to sample.
	Jobs func() ([]PlacementJob, error)
	// SireniaFormations reads where each sirenia app's engines run for the
	// sirenia-formation line. Nil adds no line.
	SireniaFormations func() ([]updaterdeploy.SireniaFormationState, error)
	// ZFS and Overlay add the rows that read the host the check runs on,
	// after blobstore-auth and after log-sink. Nil adds no row: the
	// controller cannot see a host's pool or flannel meta file.
	ZFS     func(add func(Row))
	Overlay func(add func(Row))
}

// Collect runs every line in production:check order.
func Collect(cfg Config) []Row {
	api := cfg.API
	if api == nil {
		api = APIError{Err: errors.New("no controller client")}
	}

	var settings ct.TenancySettings
	tenancyErr := api.Get("/tenancy", &settings)

	var workload ct.WorkloadPlacement
	placementErr := api.Get("/cluster/placement", &workload)
	var (
		jobs    []PlacementJob
		jobsErr error
	)
	if cfg.Jobs != nil {
		jobs, jobsErr = cfg.Jobs()
	}

	release, releaseErr := api.GetAppRelease("blobstore")
	profiles, profileErr := api.ListRuntimeProfiles()

	var (
		hosts    []*cluster.Host
		hostsErr error
	)
	if cfg.Hosts == nil {
		hostsErr = errors.New("no host source")
	} else {
		hosts, hostsErr = cfg.Hosts()
	}

	mode := "unknown"
	var rows []Row
	add := func(row Row) {
		rows = append(rows, row)
	}

	if tenancyErr != nil {
		add(Row{
			Status: "fail",
			Check:  "tenancy",
			Detail: "error=" + tenancyErr.Error(),
			Action: "Fix the controller, then rerun. This is not self-hosted.",
		})
	} else if settings.Mode == tenancy.ModeHosted || settings.Mode == tenancy.ModeSelfHosted {
		mode = settings.Mode
		action := "Hosted mode is on. Quotas apply."
		if mode == tenancy.ModeSelfHosted {
			action = "Quotas are unlimited. Run flynn-host tenancy:mode hosted to switch."
		}
		add(Row{Status: "ok", Check: "tenancy", Detail: "mode=" + mode, Action: action})
		// The tenancy row stays the stored mode. Later lines use AssumeMode
		// so a switch can be judged before the row is written.
		if cfg.AssumeMode == tenancy.ModeHosted || cfg.AssumeMode == tenancy.ModeSelfHosted {
			mode = cfg.AssumeMode
		}
	} else {
		add(Row{
			Status: "fail",
			Check:  "tenancy",
			Detail: "error=unknown tenancy mode",
			Action: "Set it: flynn-host tenancy:mode self_hosted or hosted.",
		})
	}

	addPlacementLine(add, api, workload, placementErr, hosts, hostsErr, jobs, jobsErr)
	addBlobstoreLine(add, mode, release, releaseErr)
	addBlobstoreAuthLine(add, release, releaseErr)
	if cfg.ZFS != nil {
		cfg.ZFS(add)
	}
	addReserveLine(add, mode, profiles, profileErr)
	addHostCapLine(add, hosts, hostsErr)
	addSingletonCapLine(add, hosts, hostsErr)
	addPostgresAvailabilityLine(add, api, hosts, hostsErr)
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "mysql-availability", "/cluster/mysql-availability", "mysql:availability", 2, "The MySQL page shows that.")
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "redis-availability", "/cluster/redis-availability", "redis:availability", 2, "The Redis page shows the offset loss.")
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "mongodb-availability", "/cluster/mongodb-availability", "mongodb:availability", 3, "The MongoDB page says when failover is unavailable.")
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "kafka-availability", "/cluster/kafka-availability", "kafka:availability", 3, "The Kafka page says when failover is unavailable.")
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "clickhouse-availability", "/cluster/clickhouse-availability", "clickhouse:availability", 3, "The ClickHouse page says when failover is unavailable.")
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	addBackupLine(add, api, now)
	if cfg.SireniaFormations != nil {
		states, err := cfg.SireniaFormations()
		addSireniaFormationLine(add, states, err)
	}
	addLogSinkLine(add, api, mode)
	if cfg.Overlay != nil {
		cfg.Overlay(add)
	}
	addServiceTokenLine(add, api, now)
	return rows
}

// Failing names the checks whose status is fail, in order.
func Failing(rows []Row) []string {
	var fails []string
	for _, row := range rows {
		if row.Status == "fail" {
			fails = append(fails, row.Check)
		}
	}
	return fails
}

// HostedBlockError is HostedBlock's refusal. Fails names the failing checks.
type HostedBlockError struct {
	Fails []string
}

func (e *HostedBlockError) Error() string {
	return fmt.Sprintf("refusing hosted: production:check would fail (%s); run flynn-host production:check", strings.Join(e.Fails, ", "))
}

// HostedBlock reports whether the check would fail if the cluster were
// already hosted. Warn rows do not block.
func HostedBlock(cfg Config) error {
	cfg.AssumeMode = tenancy.ModeHosted
	if fails := Failing(Collect(cfg)); len(fails) > 0 {
		return &HostedBlockError{Fails: fails}
	}
	return nil
}

// JobHost is a host whose running jobs the placement line samples.
// *cluster.Host and the controller's host clients have both methods.
type JobHost interface {
	Tags() map[string]string
	ListActiveJobs() (map[string]host.ActiveJob, error)
}

// ListPlacementJobs samples every host's running jobs for the placement line.
func ListPlacementJobs(hosts []JobHost) ([]PlacementJob, error) {
	var out []PlacementJob
	for _, h := range hosts {
		if h == nil {
			continue
		}
		jobs, err := h.ListActiveJobs()
		if err != nil {
			return nil, err
		}
		for _, job := range jobs {
			if job.Job == nil {
				continue
			}
			meta := job.Job.Metadata
			out = append(out, PlacementJob{
				HostTags: h.Tags(),
				AppID:    meta[host.MetaControllerApp],
				AppName:  meta[host.MetaControllerAppName],
				Type:     meta[host.MetaControllerType],
				Meta:     meta,
			})
		}
	}
	return out, nil
}
