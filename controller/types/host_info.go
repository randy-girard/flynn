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
