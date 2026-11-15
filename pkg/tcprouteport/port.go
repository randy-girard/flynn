// Package tcprouteport is the public TCP port window for router routes.
// It is not the per-instance database allocator. FLYNN_INSTANCE_PORT stays
// in 3000–3500 and is opened only on the hosts running that instance.
package tcprouteport

import "fmt"

const (
	// MinPort and MaxPort are inclusive. The window is unprivileged, above
	// the cluster-wide host binds (22, 53, 80, 443, 1111, 1113, 3000–3500,
	// 5000, 5002), and below the Linux default ephemeral range 32768–60999.
	MinPort = 10000
	MaxPort = 29999

	// AllocLock serializes automatic inserts. An explicit port skips it.
	// tcp_routes_port_key (UNIQUE port WHERE deleted_at IS NULL) is the
	// backstop if an explicit insert races an automatic one.
	AllocLock int64 = 83420089
)

// ErrWindow is returned when a caller asks for a TCP route port outside
// MinPort–MaxPort. Port 0 means "assign" and is not this error.
var ErrWindow = fmt.Errorf("tcp route port must be in %d-%d", MinPort, MaxPort)

// ErrExhausted is returned when every port in the window is in use by a
// live route. The allocator does not wrap below MinPort.
var ErrExhausted = fmt.Errorf("tcp route ports %d-%d are exhausted", MinPort, MaxPort)

// Allowed reports whether port may be stored on a new TCP route.
func Allowed(port int) bool {
	return port >= MinPort && port <= MaxPort
}

// Check accepts 0 (assign the lowest free port) and ports inside the window.
func Check(port int) error {
	if port == 0 || Allowed(port) {
		return nil
	}
	return ErrWindow
}

// Count is the number of integers in the inclusive window.
func Count() int {
	return MaxPort - MinPort + 1
}
