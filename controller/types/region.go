package types

// RegionLink is one other cluster this controller can name. It is not a host
// in this Raft group. The cluster key is never stored here.
type RegionLink struct {
	Region        string `json:"region"`
	Domain        string `json:"domain"`
	ControllerURL string `json:"controller_url"`
	DashboardURL  string `json:"dashboard_url"`
	TLSPin        string `json:"tls_pin"`
}

// ClusterRegionList is this cluster's region plus the linked clusters.
// Links is empty, not null, when there are none.
type ClusterRegionList struct {
	Region string       `json:"region"`
	Links  []RegionLink `json:"links"`
}
