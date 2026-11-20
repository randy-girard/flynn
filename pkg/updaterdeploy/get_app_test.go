package updaterdeploy

import (
	"errors"
	"fmt"
	"testing"

	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
)

type fakeAppGetter struct {
	errs  []error
	calls int
}

func (f *fakeAppGetter) GetApp(name string) (*ct.App, error) {
	f.calls++
	if f.calls <= len(f.errs) {
		return nil, f.errs[f.calls-1]
	}
	return &ct.App{Name: name}, nil
}

func noAppRetryDelay(t *testing.T) {
	t.Helper()
	prev := getAppRetryDelay
	getAppRetryDelay = 0
	t.Cleanup(func() { getAppRetryDelay = prev })
}

// TestGetAppWithRetryRidesOutOverlayBlip: `script/vagrant.sh update` failed
// with "error getting app ... timeout awaiting response headers" on the
// first GetApp after ReplaceHostInfraJobs swapped the last flannel job.
func TestGetAppWithRetryRidesOutOverlayBlip(t *testing.T) {
	noAppRetryDelay(t)
	timeout := errors.New(`Get "http://controller.discoverd/apps/blobstore": net/http: timeout awaiting response headers`)
	f := &fakeAppGetter{errs: []error{timeout, timeout}}
	app, err := GetAppWithRetry(f, "blobstore", nil)
	if err != nil {
		t.Fatalf("transient controller errors must be retried: %v", err)
	}
	if app == nil || app.Name != "blobstore" || f.calls != 3 {
		t.Fatalf("app=%v calls=%d", app, f.calls)
	}
}

func TestGetAppWithRetryDoesNotRetryNotFoundOr401(t *testing.T) {
	noAppRetryDelay(t)
	for _, err := range []error{
		controller.ErrNotFound,
		fmt.Errorf(`GET "http://controller.discoverd/apps/x": httpclient: raw req: unexpected status 401`),
	} {
		f := &fakeAppGetter{errs: []error{err, err, err}}
		if _, got := GetAppWithRetry(f, "x", nil); got != err {
			t.Errorf("got %v, want %v", got, err)
		}
		if f.calls != 1 {
			t.Errorf("%v retried %d times", err, f.calls)
		}
	}
}

func TestGetAppWithRetryGivesUp(t *testing.T) {
	noAppRetryDelay(t)
	boom := errors.New("connection refused")
	f := &fakeAppGetter{errs: []error{boom, boom, boom, boom, boom, boom, boom, boom, boom, boom}}
	if _, err := GetAppWithRetry(f, "x", nil); err != boom {
		t.Fatalf("got %v", err)
	}
	if f.calls != getAppRetryAttempts {
		t.Fatalf("calls=%d", f.calls)
	}
}
