// Package buildadmit decides which git compiles may be created and which
// pending compiles may be started. The same rules apply on a hosted cluster
// and on a self-hosted cluster. tenancy_mode is not an input.
package buildadmit

import (
	"errors"
	"sort"
	"strings"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/host/resource"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

const (
	// DefaultClusterLimit is how many compiles may be in flight on the cluster.
	DefaultClusterLimit = ct.DefaultBuildClusterLimit
	// DefaultAccountLimit is how many of those one owner_account may hold.
	// It is below the cluster limit so a second account can still build.
	DefaultAccountLimit = ct.DefaultBuildAccountLimit
	// GitPushWaits is true: after the controller accepts a build, gitreceive
	// stays up until the compile finishes. A push that is over the cap fails
	// immediately and does not wait.
	GitPushWaits = true
	// MaxPendingWait bounds how long an accepted build may sit pending.
	MaxPendingWait = 30 * time.Minute
	// TokenPath is the root-only file that receives the build token.
	TokenPath = "/run/secrets/controller_token"
	// CreateLockID is the transaction advisory lock that serializes build creates.
	CreateLockID = 831007
)

const (
	StatePending  = "pending"
	StateBlocked  = "blocked"
	StateStarting = "starting"
	StateUp       = "up"
	StateStopping = "stopping"
	StateDown     = "down"
)

// Limits is the cluster cap and the per-account cap.
type Limits struct {
	Cluster int
	Account int
}

// Job is one in-flight or finished compile, as the cap sees it.
type Job struct {
	ID        string
	Type      string
	Owner     string
	State     string
	CreatedAt time.Time
}

// LimitKind says which cap rejected a create.
type LimitKind string

const (
	LimitCluster LimitKind = "cluster"
	LimitAccount LimitKind = "account"
)

// CapacityError is a full build cap. The HTTP handler returns it as 429
// with retry false.
type CapacityError struct {
	Kind LimitKind
}

func (e CapacityError) Error() string {
	if e.Kind == LimitAccount {
		return "account build limit reached"
	}
	return "cluster build limit reached"
}

// HostJobInput is the compile the scheduler is about to AddJob.
// ReleaseEnv is accepted so a caller can pass the app env and tests can
// prove it is not copied. Config vars travel on stdin.
type HostJobInput struct {
	HostID     string
	UUID       string
	AppID      string
	AppName    string
	ReleaseID  string
	Type       string
	Owner      string
	SystemApp  bool
	Args       []string
	Env        map[string]string
	ReleaseEnv map[string]string
	Token      string
	Resources  resource.Resources
}

// VolumeRef is a volume binding the scheduler already recorded.
type VolumeRef struct {
	ID           string
	HostID       string
	Path         string
	DeleteOnStop bool
}

// PlacementJob is the controller row gitreceive polls.
type PlacementJob struct {
	ID     string
	HostID string
	State  string
	Type   string
}

var (
	// ErrPlacementStopped means the build was marked down before it started.
	ErrPlacementStopped = errors.New("build job stopped before it was placed")
	// ErrPlacementHost means the cluster job id is not for the recorded host.
	ErrPlacementHost = errors.New("build job id is not on the recorded host")
	// ErrPlacementTimeout means the build was still unplaced at the deadline.
	ErrPlacementTimeout = errors.New("build was not placed before the deadline")
)

// NormalizeOwner trims owner_account. Empty and whitespace share one bucket.
func NormalizeOwner(owner string) string {
	return strings.TrimSpace(owner)
}

// StoredLimits returns a usable pair. Corrupt or unlimited stored values
// fall back to the defaults, not to unlimited.
func StoredLimits(cluster, account int) Limits {
	c, a := ct.NormalizeBuildLimits(cluster, account)
	return Limits{Cluster: c, Account: a}
}

// ValidLimits reports whether an operator may store this pair.
// There is no unlimited value. Account is at least 1 and strictly below cluster.
func ValidLimits(l Limits) error {
	if _, _, ok := ct.CheckBuildLimits(l.Cluster, l.Account); !ok {
		return errors.New("build_cluster_limit must be at least 2 and build_account_limit must be at least 1 and less than the cluster limit")
	}
	return nil
}

// CanCreate reports whether a new compile for owner may be inserted.
// existing does not include the new job. Pending, blocked, starting, and up
// slugbuilder and dockerbuilder jobs count. cluster:admin is not exempt.
func CanCreate(existing []Job, owner string, limits Limits) error {
	limits = StoredLimits(limits.Cluster, limits.Account)
	cluster, account := inflightCounts(existing, NormalizeOwner(owner))
	if cluster >= limits.Cluster {
		return CapacityError{Kind: LimitCluster}
	}
	if account >= limits.Account {
		return CapacityError{Kind: LimitAccount}
	}
	return nil
}

// SelectPending returns the pending compiles that may be started, oldest
// first. Running and blocked compiles already occupy slots. An account at
// its cap does not take a free cluster slot ahead of another account.
func SelectPending(jobs []Job, limits Limits) []Job {
	limits = StoredLimits(limits.Cluster, limits.Account)
	clusterUsed := 0
	ownerUsed := map[string]int{}
	var waiting []Job
	for _, job := range jobs {
		if !counts(job) {
			continue
		}
		owner := NormalizeOwner(job.Owner)
		if job.State == StatePending {
			waiting = append(waiting, job)
			continue
		}
		clusterUsed++
		ownerUsed[owner]++
	}
	sort.SliceStable(waiting, func(i, j int) bool {
		if waiting[i].CreatedAt.Equal(waiting[j].CreatedAt) {
			return waiting[i].ID < waiting[j].ID
		}
		return waiting[i].CreatedAt.Before(waiting[j].CreatedAt)
	})
	var start []Job
	for _, job := range waiting {
		owner := NormalizeOwner(job.Owner)
		if clusterUsed >= limits.Cluster || ownerUsed[owner] >= limits.Account {
			continue
		}
		clusterUsed++
		ownerUsed[owner]++
		start = append(start, job)
	}
	return start
}

func counts(job Job) bool {
	if !ct.IsBuilderProcessType(job.Type) {
		return false
	}
	switch job.State {
	case StatePending, StateBlocked, StateStarting, StateUp:
		return true
	default:
		return false
	}
}

func inflightCounts(jobs []Job, owner string) (cluster, account int) {
	for _, job := range jobs {
		if !counts(job) {
			continue
		}
		cluster++
		if NormalizeOwner(job.Owner) == owner {
			account++
		}
	}
	return cluster, account
}

// HostJob is the host config for a compile. It does not copy ReleaseEnv,
// does not set the system partition, and does not mount a token unless one
// was just minted.
func HostJob(in HostJobInput) *host.Job {
	env := map[string]string{}
	for k, v := range in.Env {
		if k == "CONTROLLER_KEY" {
			continue
		}
		env[k] = v
	}
	meta := map[string]string{
		"flynn-controller.app":      in.AppID,
		"flynn-controller.app_name": in.AppName,
		"flynn-controller.release":  in.ReleaseID,
		"flynn-controller.type":     in.Type,
		ct.MetaBuildOwner:           NormalizeOwner(in.Owner),
	}
	job := &host.Job{
		ID:        cluster.GenerateJobID(in.HostID, in.UUID),
		Partition: string(ct.PartitionTypeBackground),
		Metadata:  meta,
		Resources: in.Resources,
		Config: host.ContainerConfig{
			Args:  append([]string(nil), in.Args...),
			Env:   env,
			Stdin: true,
		},
	}
	if in.Token != "" {
		job.Config.Secrets = []host.ContainerSecret{{
			Path: TokenPath,
			Data: []byte(in.Token),
		}}
	}
	return job
}

// ForeignCompile reports that this host must reject the AddJob. Only
// slugbuilder and dockerbuilder types are checked. The OS builder app is
// not a compile.
func ForeignCompile(processType, jobID, thisHost string) bool {
	if !ct.IsBuilderProcessType(processType) {
		return false
	}
	hostID, err := cluster.ExtractHostID(jobID)
	if err != nil {
		return true
	}
	return hostID != thisHost
}

// JobOnHost reports that the cluster job id was generated for hostID.
func JobOnHost(jobID, hostID string) bool {
	got, err := cluster.ExtractHostID(jobID)
	return err == nil && got == hostID && hostID != ""
}

// KeepVolumes drops ephemeral volumes bound to a host that this placement
// is not using. Placement clears HostID before it picks again, so a bound
// /tmp from the previous attempt must not be reused. Persistent volumes stay.
func KeepVolumes(vols []VolumeRef, placingHost string) []VolumeRef {
	var kept []VolumeRef
	for _, vol := range vols {
		if vol.DeleteOnStop && vol.HostID != "" && vol.HostID != placingHost {
			continue
		}
		kept = append(kept, vol)
	}
	return kept
}

// WaitForPlacement polls until the build is starting on the host encoded in
// its job id, or the job has stopped, or the deadline has passed.
// A pending row with a host id is not ready: gitreceive attaches once the
// process is starting.
func WaitForPlacement(get func() (PlacementJob, error), deadline time.Time, now func() time.Time, sleep func(time.Duration)) (PlacementJob, error) {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	var last PlacementJob
	for {
		job, err := get()
		if err != nil {
			return PlacementJob{}, err
		}
		last = job
		if err := placementError(job); err != nil {
			return job, err
		}
		if placementReady(job) {
			return job, nil
		}
		if !now().Before(deadline) {
			return last, ErrPlacementTimeout
		}
		sleep(200 * time.Millisecond)
	}
}

func placementReady(job PlacementJob) bool {
	if job.ID == "" || job.HostID == "" || !JobOnHost(job.ID, job.HostID) {
		return false
	}
	return job.State == StateStarting || job.State == StateUp
}

func placementError(job PlacementJob) error {
	switch job.State {
	case StateDown, "crashed", "failed":
		return ErrPlacementStopped
	}
	if job.ID != "" && job.HostID != "" && !JobOnHost(job.ID, job.HostID) {
		return ErrPlacementHost
	}
	return nil
}
