package updaterdeploy

import (
	"fmt"
	"time"
)

// Overridable for tests. These bound non-streaming controller RPCs used
// during host-update repair (VolumeList, JobList, AppList, PutVolume).
// The streaming deploy client must not inherit these values.
var (
	repairCallTimeout  = 15 * time.Second
	repairCallAttempts = 3
	repairCallDelay    = time.Second
)

func callWithTimeout(d time.Duration, fn func() error) error {
	if fn == nil {
		return nil
	}
	if d <= 0 {
		return fn()
	}
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	select {
	case err := <-ch:
		return err
	case <-time.After(d):
		return fmt.Errorf("timed out after %s", d)
	}
}

func callWithRetry(fn func() error) error {
	var err error
	for i := 0; i < repairCallAttempts; i++ {
		if i > 0 && repairCallDelay > 0 {
			time.Sleep(repairCallDelay)
		}
		err = callWithTimeout(repairCallTimeout, fn)
		if err == nil {
			return nil
		}
	}
	if err == nil {
		return fmt.Errorf("repair call failed after %d attempts", repairCallAttempts)
	}
	return err
}
