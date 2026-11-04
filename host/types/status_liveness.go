package host

// Liveness is the /host/status body for a caller who has not presented the
// host auth key. Bootstrap still needs the host id and advertised URL.
// Discoverd and flannel job ids, the discoverd address, and the overlay
// subnet are omitted. The stored status is not modified.
func (s *HostStatus) Liveness() *HostStatus {
	if s == nil {
		return nil
	}
	out := *s
	out.Discoverd = nil
	out.Network = nil
	return &out
}
