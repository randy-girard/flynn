package host

import (
	"errors"
	"os"
	"time"

	"github.com/flynn/flynn/host/resource"
)

// TagPrefix is the prefix added to tags in discoverd instance metadata
const TagPrefix = "tag:"

const DiffPath = "/.container-diff"

type Job struct {
	ID string `json:"id,omitempty"`

	Mountspecs []*Mountspec `json:"mountspecs,omitempty"`

	Metadata map[string]string `json:"metadata,omitempty"`

	Resources resource.Resources `json:"resources,omitempty"`
	Partition string             `json:"partition,omitempty"`

	Config ContainerConfig `json:"config,omitempty"`

	// If Resurrect is true, the host service will attempt to start the job when
	// starting after stopping (via crash or shutdown) with the job running.
	Resurrect bool `json:"resurrect,omitempty"`

	Profiles []JobProfile `json:"profiles,omitempty"`
}

func (j *Job) Dup() *Job {
	job := *j

	dupMap := func(m map[string]string) (res map[string]string) {
		if m != nil {
			res = make(map[string]string, len(m))
		}
		for k, v := range m {
			res[k] = v
		}
		return res
	}
	dupSlice := func(s []string) (res []string) {
		if s != nil {
			res = make([]string, len(s))
		}
		for i, v := range s {
			res[i] = v
		}
		return res
	}
	job.Metadata = dupMap(j.Metadata)
	job.Config.Args = dupSlice(j.Config.Args)
	job.Config.Env = dupMap(j.Config.Env)
	if j.Config.Ports != nil {
		job.Config.Ports = make([]Port, len(j.Config.Ports))
		for i, p := range j.Config.Ports {
			job.Config.Ports[i] = p
		}
	}
	if j.Config.Mounts != nil {
		job.Config.Mounts = make([]Mount, len(j.Config.Mounts))
		for i, m := range j.Config.Mounts {
			job.Config.Mounts[i] = m
		}
	}

	return &job
}

type JobProfile string

const (
	JobProfileZFS  JobProfile = "zfs"
	JobProfileKVM  JobProfile = "kvm"
	JobProfileLoop JobProfile = "loop"
)

type MountspecType string

const MountspecTypeSquashfs MountspecType = "squashfs"

type Mountspec struct {
	Type   MountspecType     `json:"type,omitempty"`
	ID     string            `json:"id,omitempty"`
	URL    string            `json:"url,omitempty"`
	Size   int64             `json:"size,omitempty"`
	Hashes map[string]string `json:"hashes,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

type JobResources struct {
	Memory int `json:"memory,omitempty"` // in KiB
}

type Device struct {
	// Device type, block, char, etc.
	Type rune `json:"type"`

	// Path to the device.
	Path string `json:"path"`

	// Major is the device's major number.
	Major int64 `json:"major"`

	// Minor is the device's minor number.
	Minor int64 `json:"minor"`

	// Cgroup permissions format, rwm.
	Permissions string `json:"permissions"`

	// FileMode permission bits for the device.
	FileMode os.FileMode `json:"file_mode"`

	// Uid of the device.
	Uid uint32 `json:"uid"`

	// Gid of the device.
	Gid uint32 `json:"gid"`

	// Write the file to the allowed list
	Allow bool `json:"allow"`
}

type ContainerConfig struct {
	Args               []string          `json:"args,omitempty"`
	TTY                bool              `json:"tty,omitempty"`
	Stdin              bool              `json:"stdin,omitempty"`
	Data               bool              `json:"data,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	Mounts             []Mount           `json:"mounts,omitempty"`
	Volumes            []VolumeBinding   `json:"volumes,omitempty"`
	Ports              []Port            `json:"ports,omitempty"`
	WorkingDir         string            `json:"working_dir,omitempty"`
	Uid                *uint32           `json:"uid,omitempty"`
	Gid                *uint32           `json:"gid,omitempty"`
	HostNetwork        bool              `json:"host_network,omitempty"`
	HostPIDNamespace   bool              `json:"host_pid_namespace,omitempty"`
	DisableLog         bool              `json:"disable_log,omitempty"`
	LinuxCapabilities  *[]string         `json:"linux_capabilities,omitempty"`
	AllowedDevices     *[]*Device        `json:"allowed_devices,omitempty"`
	AutoCreatedDevices *[]*Device        `json:"auto_created_devices,omitempty"`
	WriteableCgroups   bool              `json:"writeable_cgroups,omitempty"`
}

// Apply 'y' to 'x', returning a new structure.  'y' trumps.
func (x ContainerConfig) Merge(y ContainerConfig) ContainerConfig {
	x.TTY = x.TTY || y.TTY
	x.Stdin = x.Stdin || y.Stdin
	x.Data = x.Data || y.Data
	if y.Args != nil {
		x.Args = y.Args
	}
	env := make(map[string]string, len(x.Env)+len(y.Env))
	for k, v := range x.Env {
		env[k] = v
	}
	for k, v := range y.Env {
		env[k] = v
	}
	x.Env = env
	mounts := make([]Mount, 0, len(x.Mounts)+len(y.Mounts))
	mounts = append(mounts, x.Mounts...)
	mounts = append(mounts, y.Mounts...)
	x.Mounts = mounts
	volumes := make([]VolumeBinding, 0, len(x.Volumes)+len(y.Volumes))
	volumes = append(volumes, x.Volumes...)
	volumes = append(volumes, y.Volumes...)
	x.Volumes = volumes
	ports := make([]Port, 0, len(x.Ports)+len(y.Ports))
	ports = append(ports, x.Ports...)
	ports = append(ports, y.Ports...)
	x.Ports = ports
	if y.WorkingDir != "" {
		x.WorkingDir = y.WorkingDir
	}
	if y.Uid != nil {
		x.Uid = y.Uid
	}
	if y.Gid != nil {
		x.Gid = y.Gid
	}
	// SEC-008: HostNetwork and HostPIDNamespace are security-sensitive flags
	// that should only be set explicitly, not escalated via OR merge.
	// The merged config uses y's values if set, otherwise keeps x's values.
	// This prevents a lower-privilege config from gaining host namespace access
	// by merging with any config that happens to have them enabled.
	if y.HostNetwork {
		x.HostNetwork = y.HostNetwork
	}
	if y.HostPIDNamespace {
		x.HostPIDNamespace = y.HostPIDNamespace
	}
	return x
}

type Port struct {
	Port    int      `json:"port,omitempty"`
	Proto   string   `json:"proto,omitempty"`
	Service *Service `json:"service,omitempty"`
}

type Service struct {
	Name string `json:"name,omitempty"`
	// Create the service in service discovery
	Create bool         `json:"create,omitempty"`
	Check  *HealthCheck `json:"check,omitempty"`
}

type HealthCheck struct {
	// Type is one of tcp, http, https
	Type string `json:"type,omitempty"`
	// Interval is the time to wait between checks after the service has been
	// marked as up. It defaults to two seconds.
	Interval time.Duration `json:"interval,omitempty"`
	// Threshold is the number of consecutive checks of the same status before
	// a service will be marked as up or down after coming up for the first
	// time. It defaults to 2.
	Threshold int `json:"threshold,omitempty"`
	// If KillDown is true, the job will be killed if the service goes down (or
	// does not come up)
	KillDown bool `json:"kill_down,omitempty"`
	// StartTimeout is the maximum duration that a service can take to come up
	// for the first time if KillDown is true. It defaults to ten seconds.
	StartTimeout time.Duration `json:"start_timeout,omitempty"`

	// Extra optional config fields for http/https checks
	Path   string `json:"path,omitempty"`
	Host   string `json:"host,omitempty"`
	Match  string `json:"match,omitempty"`
	Status int    `json:"status,omitempty"`
}

type Mount struct {
	Location  string `json:"location,omitempty"`
	Target    string `json:"target,omitempty"`
	Writeable bool   `json:"writeable,omitempty"`
	Device    string `json:"device,omitempty"`
	Data      string `json:"data,omitempty"`
	Flags     int    `json:"flags,omitempty"`
}

type VolumeBinding struct {
	// Target defines the filesystem path inside the container where the volume will be mounted.
	Target string `json:"target"`
	// VolumeID can be thought of as the source path if this were a simple bind-mount.  It is resolved by a VolumeManager.
	VolumeID     string `json:"volume"`
	Writeable    bool   `json:"writeable"`
	DeleteOnStop bool   `json:"delete_on_stop"`
}

type Host struct {
	ID string `json:"id,omitempty"`

	Jobs     []*Job            `json:"jobs,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type Event struct {
	Event JobEventType `json:"event,omitempty"`
	JobID string       `json:"job_id,omitempty"`
	Job   *ActiveJob   `json:"job,omitempty"`
}

type ActiveJob struct {
	Job        *Job      `json:"job,omitempty"`
	HostID     string    `json:"host_id,omitempty"`
	InternalIP string    `json:"internal_ip,omitempty"`
	PID        *int      `json:"pid,omitempty"`
	ForceStop  bool      `json:"force_stop,omitempty"`
	Status     JobStatus `json:"status,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	ExitStatus *int      `json:"exit_status,omitempty"`
	Error      *string   `json:"error,omitempty"`
}

func (j *ActiveJob) Dup() *ActiveJob {
	job := *j
	job.Job = j.Job.Dup()
	if j.ExitStatus != nil {
		*job.ExitStatus = *j.ExitStatus
	}
	if j.Error != nil {
		*job.Error = *j.Error
	}
	return &job
}

var (
	ErrJobNotRunning = errors.New("host: job not running")
	ErrAttached      = errors.New("host: job is attached")
)

type AttachReq struct {
	JobID  string     `json:"job_id,omitempty"`
	Flags  AttachFlag `json:"flags,omitempty"`
	Height uint16     `json:"height,omitempty"`
	Width  uint16     `json:"width,omitempty"`
}

type AttachFlag uint8

const (
	AttachFlagStdout AttachFlag = 1 << iota
	AttachFlagStderr
	AttachFlagStdin
	AttachFlagLogs
	AttachFlagStream
	AttachFlagInitLog
)

type JobStatus uint8

func (s JobStatus) String() string {
	return map[JobStatus]string{
		StatusStarting: "starting",
		StatusRunning:  "running",
		StatusDone:     "done",
		StatusCrashed:  "crashed",
		StatusFailed:   "failed",
	}[s]
}

const (
	StatusStarting JobStatus = iota
	StatusRunning
	StatusDone
	StatusCrashed
	StatusFailed
)

const (
	AttachSuccess byte = iota
	AttachWaiting
	AttachError
	AttachData
	AttachSignal
	AttachExit
	AttachResize
)

type NetworkConfig struct {
	JobID     string   `json:"job_id"`
	Subnet    string   `json:"subnet"`
	MTU       int      `json:"mtu"`
	Resolvers []string `json:"resolvers"`
}

type DiscoverdConfig struct {
	JobID string `json:"job_id"`
	URL   string `json:"url"`
	DNS   string `json:"dns"`
}

type HostStatus struct {
	ID        string            `json:"id"`
	Tags      map[string]string `json:"tags,omitempty"`
	PID       int               `json:"pid"`
	URL       string            `json:"url"`
	Discoverd *DiscoverdConfig  `json:"discoverd,omitempty"`
	Network   *NetworkConfig    `json:"network,omitempty"`
	Version   string            `json:"version"`
	Flags     []string          `json:"flags"`
}

type JobEventType string

const (
	JobEventCreate  JobEventType = "create"
	JobEventStart   JobEventType = "start"
	JobEventStop    JobEventType = "stop"
	JobEventError   JobEventType = "error"
	JobEventCleanup JobEventType = "cleanup"
)

type ResourceCheck struct {
	Ports []Port `json:"ports,omitempty"`
}

type Command struct {
	Path          string         `json:"path"`
	Args          []string       `json:"args"`
	PID           int            `json:"pid"`
	ShutdownDelay *time.Duration `json:"shutdown_delay,omitempty"`
}

type LogBuffers map[string]LogBuffer

type LogBuffer map[string]string

// ContainerStats contains runtime resource usage for a container/job.
// These stats are collected from cgroups and network interfaces.
type ContainerStats struct {
	JobID     string    `json:"job_id"`
	Timestamp time.Time `json:"timestamp"`

	// CPU stats (from cgroups cpuacct/cpu.stat)
	CPUUsageNanoseconds uint64  `json:"cpu_usage_nanoseconds"`
	CPUUsagePercent     float64 `json:"cpu_usage_percent,omitempty"` // Calculated if previous sample available
	CPUThrottledPeriods uint64  `json:"cpu_throttled_periods"`
	CPUThrottledTimeNs  uint64  `json:"cpu_throttled_time_ns"`

	// Memory stats (from cgroups memory)
	MemoryUsageBytes uint64 `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64 `json:"memory_limit_bytes"`
	MemoryMaxUsage   uint64 `json:"memory_max_usage_bytes"`
	MemoryCacheBytes uint64 `json:"memory_cache_bytes"`
	MemoryRSSBytes   uint64 `json:"memory_rss_bytes"`

	// Network stats (from veth interface)
	NetworkRxBytes   uint64 `json:"network_rx_bytes"`
	NetworkTxBytes   uint64 `json:"network_tx_bytes"`
	NetworkRxPackets uint64 `json:"network_rx_packets"`
	NetworkTxPackets uint64 `json:"network_tx_packets"`

	// I/O stats (from cgroups blkio/io)
	IOReadBytes  uint64 `json:"io_read_bytes"`
	IOWriteBytes uint64 `json:"io_write_bytes"`

	// PIDs (from cgroups pids)
	PIDsCurrent uint64 `json:"pids_current"`
	PIDsLimit   uint64 `json:"pids_limit"`
}

// HostResourceStats contains aggregated resource usage for the host.
// These stats are collected from /proc and system calls.
type HostResourceStats struct {
	HostID    string    `json:"host_id"`
	Timestamp time.Time `json:"timestamp"`

	// CPU stats
	CPUUsagePercent float64 `json:"cpu_usage_percent"`
	CPUCount        int     `json:"cpu_count"`

	// Memory stats (from /proc/meminfo)
	MemoryTotalBytes     uint64 `json:"memory_total_bytes"`
	MemoryUsedBytes      uint64 `json:"memory_used_bytes"`
	MemoryAvailableBytes uint64 `json:"memory_available_bytes"`
	MemoryFreeBytes      uint64 `json:"memory_free_bytes"`
	MemoryCachedBytes    uint64 `json:"memory_cached_bytes"`
	MemoryBuffersBytes   uint64 `json:"memory_buffers_bytes"`

	// Disk stats
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskUsedBytes  uint64 `json:"disk_used_bytes"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`

	// Network stats (aggregate across interfaces)
	NetworkRxBytes   uint64 `json:"network_rx_bytes"`
	NetworkTxBytes   uint64 `json:"network_tx_bytes"`
	NetworkRxPackets uint64 `json:"network_rx_packets"`
	NetworkTxPackets uint64 `json:"network_tx_packets"`

	// Load average (from /proc/loadavg)
	LoadAvg1  float64 `json:"load_avg_1"`
	LoadAvg5  float64 `json:"load_avg_5"`
	LoadAvg15 float64 `json:"load_avg_15"`

	// Uptime in seconds (from /proc/uptime)
	UptimeSeconds float64 `json:"uptime_seconds"`

	// Job counts
	RunningJobsCount int `json:"running_jobs_count"`
	TotalJobsCount   int `json:"total_jobs_count"`
}

// AllJobsStats contains stats for all jobs on a host
type AllJobsStats struct {
	HostID    string            `json:"host_id"`
	Timestamp time.Time         `json:"timestamp"`
	Jobs      []*ContainerStats `json:"jobs"`
}

// WebhookConfig represents a configured webhook endpoint
type WebhookConfig struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// WebhookEvent is the payload sent to webhook endpoints
type WebhookEvent struct {
	EventID     string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	HostID      string            `json:"host_id"`
	Code        string            `json:"code"`
	Description string            `json:"description"`
	Severity    string            `json:"severity"`     // "info", "warning", "error", "critical"
	JobID       string            `json:"job_id,omitempty"`
	Job         *ActiveJob        `json:"job,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// Webhook event severity levels
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
)

// Webhook event codes

// H-codes: Job/Container lifecycle events
const (
	CodeJobCreate      = "H10" // Job created
	CodeJobStart       = "H11" // Job started (running)
	CodeJobStop        = "H12" // Job stopped (exit 0)
	CodeJobCrash       = "H13" // Job crashed (non-zero exit)
	CodeJobFailed      = "H14" // Job failed to start
	CodeJobCleanup     = "H15" // Job cleaned up
	CodeMemorySoft     = "H20" // Soft memory limit exceeded
	CodeMemoryHard     = "H21" // Hard memory limit exceeded (OOM kill)
)

// R-codes: Runtime events
const (
	CodeMountFailure   = "R10" // Squashfs mount/verification failure
)

// D-codes: Daemon lifecycle events
const (
	CodeDaemonStart    = "D10" // Daemon started
	CodeDaemonShutdown = "D11" // Daemon shutting down
	CodeDaemonUpdate   = "D12" // Daemon zero-downtime update initiated
)
