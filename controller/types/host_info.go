package types

// HostInfo is one host of the cluster as GET /hosts returns it.
type HostInfo struct {
	ID           string            `json:"id"`
	Tags         map[string]string `json:"tags"`
	Addr         string            `json:"addr"`
	MaxProcesses *int              `json:"max_processes,omitempty"`
	// Shutdown is set while the host drains: discoverd still lists it
	// until its down event, but the scheduler places nothing there.
	Shutdown bool `json:"shutdown,omitempty"`
}

// ClusterCapacity is GET /cluster/capacity: each host's CPU and memory
// totals and what reserving jobs hold on it, judged with the scheduler's
// rule (placement.Reservations). The deployer reads it before a roll stops
// an old job, so a replacement that cannot be placed fails the deploy
// before anything is stopped (NEW-45).
type ClusterCapacity struct {
	TenancyMode string `json:"tenancy_mode"`
	// ReserveProfiles is RuntimeProfile.ReserveResources by lower-case name.
	ReserveProfiles map[string]bool `json:"reserve_profiles,omitempty"`
	Hosts           []*HostCapacity `json:"hosts"`
}

// HostCapacity is one host of ClusterCapacity. MemoryTotalBytes and CPUMilli
// are zero, and StatsError is set, when the host's stats could not be read.
type HostCapacity struct {
	ID               string            `json:"id"`
	Tags             map[string]string `json:"tags,omitempty"`
	Shutdown         bool              `json:"shutdown,omitempty"`
	MemoryTotalBytes uint64            `json:"memory_total_bytes"`
	CPUMilli         int64             `json:"cpu_milli"`
	StatsError       string            `json:"stats_error,omitempty"`
	// ReservedMemoryBytes and ReservedCPUMilli are the sums of Reserved.
	ReservedMemoryBytes int64          `json:"reserved_memory_bytes"`
	ReservedCPUMilli    int64          `json:"reserved_cpu_milli"`
	Reserved            []*ReservedJob `json:"reserved,omitempty"`
}

// ReservedJob is one pending, starting, up or stopping job that holds a
// reservation on its host.
type ReservedJob struct {
	JobID       string `json:"job_id"`
	AppID       string `json:"app_id"`
	ReleaseID   string `json:"release_id"`
	Type        string `json:"type"`
	MemoryBytes int64  `json:"memory_bytes"`
	CPUMilli    int64  `json:"cpu_milli"`
}
