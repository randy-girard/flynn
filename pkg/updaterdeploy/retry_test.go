package updaterdeploy

import (
	"fmt"
	"testing"

	"github.com/randy-girard/flynn/pkg/httpclient"
)

func TestCallWithRetryDoesNotRetry401(t *testing.T) {
	prevAttempts := repairCallAttempts
	prevDelay := repairCallDelay
	prevTimeout := repairCallTimeout
	repairCallAttempts = 3
	repairCallDelay = 0
	repairCallTimeout = 0
	defer func() {
		repairCallAttempts = prevAttempts
		repairCallDelay = prevDelay
		repairCallTimeout = prevTimeout
	}()

	n := 0
	err := callWithRetry(func() error {
		n++
		return fmt.Errorf(`GET "http://controller.discoverd/volumes": httpclient: raw req: unexpected status 401`)
	})
	if err == nil {
		t.Fatal("expected 401")
	}
	if n != 1 {
		t.Fatalf("401 retried %d times", n)
	}
	if !httpclient.IsUnauthorized(err) {
		t.Fatalf("err=%v", err)
	}
}
