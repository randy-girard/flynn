package deployment

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	worker "github.com/randy-girard/flynn/controller/worker/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// realControllerError makes one JobList call with the real controller client
// against a fake controller API and returns the error it got.
func realControllerError(t *testing.T, h http.HandlerFunc) error {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	hc := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 100 * time.Millisecond, DisableKeepAlives: true}}
	c, err := controller.NewClientWithHTTP(srv.URL, "key", hc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.JobList("app")
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func jsonStatus(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
	}
}

func TestPermanentAPIErrorWithTheRealClient(t *testing.T) {
	transient := map[string]http.HandlerFunc{
		"timeout awaiting response headers": func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) },
		// retry:true is retried inside the client for 10 s first (v1 send)
		"controller 503":                      jsonStatus(503, `{"code":"service_unavailable","message":"unavailable"}`),
		"controller 500":                      jsonStatus(500, `{"code":"unknown_error","message":"Something went wrong"}`),
		"proxy 502 with a non-error body":     jsonStatus(502, `{"error":"bad gateway"}`),
		"plain 504":                           func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(504) },
		"quick JSON non-error body (EDGE-62)": jsonStatus(503, `{}`),
	}
	for name, h := range transient {
		t.Run("transient "+name, func(t *testing.T) {
			if err := realControllerError(t, h); permanentAPIError(err) {
				t.Errorf("%v classed as permanent", err)
			}
		})
	}
	permanent := map[string]http.HandlerFunc{
		"404 deleted app": jsonStatus(404, `{"code":"object_not_found","message":"app not found"}`),
		"404 plain":       func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) },
		"401":             func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) },
		"403":             jsonStatus(403, `{"code":"forbidden","message":"no"}`),
		"400 validation":  jsonStatus(400, `{"code":"validation_error","message":"bad"}`),
		"409 conflict":    jsonStatus(409, `{"code":"conflict","message":"busy"}`),
	}
	for name, h := range permanent {
		t.Run("permanent "+name, func(t *testing.T) {
			if err := realControllerError(t, h); !permanentAPIError(err) {
				t.Errorf("%v classed as transient", err)
			}
		})
	}

	// connection refused: a stale controller address
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c, _ := controller.NewClientWithHTTP("http://"+addr, "key", &http.Client{})
	if _, err := c.JobList("app"); err == nil || permanentAPIError(err) {
		t.Errorf("connection refused (%v) must be transient", err)
	}
	if !permanentAPIError(worker.ErrStopped) {
		t.Error("a stopped worker must stop the loop")
	}
}

func TestRetryAPICallBacksOffAndReportsTheLastError(t *testing.T) {
	oldB, oldMax := apiRetryBackoff, apiRetryMaxBackoff
	apiRetryBackoff, apiRetryMaxBackoff = 5*time.Millisecond, 20*time.Millisecond
	defer func() { apiRetryBackoff, apiRetryMaxBackoff = oldB, oldMax }()

	calls := 0
	err := retryAPICall("set app release", time.Second, nil, log15.New(), func() error {
		if calls++; calls < 3 {
			return errRefused
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want success on the third call", err, calls)
	}

	calls = 0
	err = retryAPICall("get formation", 100*time.Millisecond, nil, log15.New(), func() error { calls++; return err503 })
	if err == nil || calls < 3 {
		t.Fatalf("err=%v calls=%d, want a failure after retries", err, calls)
	}
	for _, want := range []string{"get formation", "100ms", "attempts", "elapsed", "last error", "catching up"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	var je httphelper.JSONError
	if !errors.As(err, &je) || je.Code != err503.Code {
		t.Errorf("error %q does not wrap the last error", err)
	}

	calls = 0
	err = retryAPICall("get release", time.Minute, nil, log15.New(), func() error { calls++; return controller.ErrNotFound })
	if err != controller.ErrNotFound || calls != 1 {
		t.Fatalf("err=%v calls=%d, want ErrNotFound unwrapped after one call", err, calls)
	}

	stop := make(chan struct{})
	close(stop)
	if err := retryAPICall("x", time.Minute, stop, nil, func() error { return errRefused }); err != worker.ErrStopped {
		t.Fatalf("stopped: err=%v", err)
	}
}
