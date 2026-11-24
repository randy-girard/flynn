package uploadlimit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/httphelper"
)

func TestParseDuration(t *testing.T) {
	const def = 60 * time.Second
	for _, c := range []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{"", def, false},
		{"  ", def, false},
		{"0", 0, false},
		{"off", 0, false},
		{"OFF", 0, false},
		{"none", 0, false},
		{"90s", 90 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"45", 45 * time.Second, false},
		{"-1s", 0, true},
		{"-5", 0, true},
		{"soon", 0, true},
	} {
		got, err := ParseDuration(c.in, def)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ParseDuration(%q) = %s, %v; want %s, err=%v", c.in, got, err, c.want, c.err)
		}
	}
}

func TestDurationFromEnvNamesTheSetting(t *testing.T) {
	t.Setenv("E36_IDLE", "soon")
	if _, err := DurationFromEnv("E36_IDLE", time.Second); err == nil || !strings.Contains(err.Error(), "E36_IDLE") {
		t.Fatalf("error does not name the setting: %v", err)
	}
	t.Setenv("E36_IDLE", "")
	if d, err := DurationFromEnv("E36_IDLE", time.Second); err != nil || d != time.Second {
		t.Fatalf("unset = %s, %v; want the default", d, err)
	}
}

// idleServer reads each request body through an IdleBody behind the same
// middleware the services use, and reports what the read ended with.
func idleServer(t *testing.T, timeout time.Duration, after func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, chan error) {
	t.Helper()
	results := make(chan error, 4)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := NewIdleBody(w, r.Body, timeout)
		_, err := io.Copy(io.Discard, body)
		body.Done()
		if body.TimedOut() {
			if !errors.Is(err, ErrIdle) {
				t.Errorf("timed out body returned %v, want ErrIdle", err)
			}
			w.Header().Set("Connection", "close")
			http.Error(w, err.Error(), http.StatusRequestTimeout)
			results <- err
			return
		}
		if after != nil {
			after(w, r)
		}
		results <- err
	})
	srv := httptest.NewUnstartedServer(httphelper.ContextInjector("test", httphelper.NewRequestLogger(h)))
	ApplyServerTimeouts(srv.Config)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, results
}

func TestIdleBodyCutsStalledBody(t *testing.T) {
	srv, results := idleServer(t, 300*time.Millisecond, nil)
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 100000\r\n\r\npartial")
	start := time.Now()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body after %s: %s", time.Since(start), err)
	}
	io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("stalled body = %d, want 408", res.StatusCode)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("cut off only after %s", d)
	}
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the connection is still open")
	}
	if err := <-results; !errors.Is(err, ErrIdle) {
		t.Fatalf("handler read ended with %v", err)
	}
}

func TestIdleBodyLetsSlowSteadyBodyThrough(t *testing.T) {
	srv, results := idleServer(t, 300*time.Millisecond, nil)
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 5; i++ {
			pw.Write([]byte("chunk"))
			time.Sleep(150 * time.Millisecond)
		}
		pw.Close()
	}()
	res, err := http.Post(srv.URL, "text/plain", pr)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("slow but steady body = %d", res.StatusCode)
	}
	if err := <-results; err != nil {
		t.Fatalf("handler read ended with %v", err)
	}
}

// Once the body is read, the work after it (unpacking a layer, a git hook)
// may take longer than the idle timeout. The deadline must not outlive the
// body: the server's background read would hit it and cancel the request.
func TestIdleBodyDeadlineEndsWithTheBody(t *testing.T) {
	const timeout = 200 * time.Millisecond
	canceled := make(chan bool, 1)
	srv, results := idleServer(t, timeout, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			canceled <- true
		case <-time.After(4 * timeout):
			canceled <- false
		}
		w.WriteHeader(200)
	})
	res, err := http.Post(srv.URL, "text/plain", strings.NewReader("whole body"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := <-results; err != nil {
		t.Fatalf("handler read ended with %v", err)
	}
	if <-canceled {
		t.Fatal("the request was canceled after its body was read: the idle deadline outlived the body")
	}
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
}

// Without a connection to set deadlines on (a ResponseRecorder), the body is
// read without one instead of failing.
func TestIdleBodyWithoutDeadlineSupport(t *testing.T) {
	rec := httptest.NewRecorder()
	body := NewIdleBody(rec, io.NopCloser(strings.NewReader("abc")), time.Second)
	b, err := io.ReadAll(body)
	body.Done()
	if err != nil || string(b) != "abc" || body.TimedOut() {
		t.Fatalf("read %q, %v, timed out %v", b, err, body.TimedOut())
	}
}

func TestApplyServerTimeouts(t *testing.T) {
	s := &http.Server{}
	ApplyServerTimeouts(s)
	if s.ReadHeaderTimeout != ReadHeaderTimeout || s.IdleTimeout != ConnIdleTimeout {
		t.Fatalf("timeouts = %s/%s", s.ReadHeaderTimeout, s.IdleTimeout)
	}
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatal("a whole-request timeout would cut off large uploads")
	}
}
