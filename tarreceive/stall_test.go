package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// EDGE-36: tarreceive cuts off layer bodies that stop arriving, bounds how
// many layers are received at once, and refuses a layer its temp disk cannot
// hold, removing the temp files on every failure.

// stallServer serves s through the same handler stack and server timeouts as
// main, with its temp files in a fresh directory.
func stallServer(t *testing.T, s *server) *httptest.Server {
	t.Helper()
	s.tempDir = t.TempDir()
	srv := httptest.NewUnstartedServer(wrapHandler(s))
	uploadlimit.ApplyServerTimeouts(srv.Config)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// stalledLayer sends the headers of a layer POST and part of its body, then
// stops sending.
func stalledLayer(t *testing.T, srv *httptest.Server, tok string, length int) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	id := strings.Repeat("ab", 32)
	fmt.Fprintf(conn, "POST /layer/%s HTTP/1.1\r\nHost: tarreceive\r\nAuthorization: Bearer %s\r\nX-Flynn-Flattened-Layer: 1\r\nContent-Length: %d\r\n\r\npartial", id, tok, length)
	return conn, bufio.NewReader(conn)
}

// readAnswer reads the response on a raw connection within d.
func readAnswer(t *testing.T, conn net.Conn, br *bufio.Reader, d time.Duration) (*http.Response, string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(d))
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer within %s: %s", d, err)
	}
	body, _ := ioutil.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

// sendLayer sends a whole layer and returns the status, body and headers.
func sendLayer(t *testing.T, srv *httptest.Server, tok string, body io.Reader, id string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/layer/"+id, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := ioutil.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

// tempEntries lists what the server left in its temp directory.
func tempEntries(t *testing.T, s *server) []string {
	t.Helper()
	entries, err := os.ReadDir(s.tempDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// waitFor polls cond for up to 5 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestCreateLayerStalledBodyTimesOut(t *testing.T) {
	s, tok := buildServer(t)
	s.idleTimeout = 300 * time.Millisecond
	var squashed int32
	s.squash = func(src, dst string) error {
		atomic.AddInt32(&squashed, 1)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}
	srv := stallServer(t, s)

	conn, br := stalledLayer(t, srv, tok, 100000)
	waitFor(t, "the stalled layer's temp file", func() bool { return len(tempEntries(t, s)) == 1 })
	start := time.Now()
	res, body := readAnswer(t, conn, br, 5*time.Second)
	if res.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("stalled layer = %d %s, want 408", res.StatusCode, body)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the stalled layer was cut off only after %s", d)
	}
	if !strings.Contains(body, "UPLOAD_IDLE_TIMEOUT") {
		t.Fatalf("error does not name the setting: %s", body)
	}
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("the connection of a timed out layer is still open")
	}
	if left := tempEntries(t, s); len(left) != 0 {
		t.Fatalf("temp files left after the timeout: %v", left)
	}
	if n := len(s.uploads); n != 0 {
		t.Fatalf("%d upload slots still held", n)
	}
	if n := s.disk.outstandingBytes(); n != 0 {
		t.Fatalf("%d bytes of disk still reserved", n)
	}
	if n := atomic.LoadInt32(&squashed); n != 0 {
		t.Fatalf("a stalled layer reached mksquashfs %d times", n)
	}

	// a layer that keeps sending, however slowly, is not cut off
	tarball, id := layerTar(t, "slow")
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < len(tarball); i += len(tarball) / 4 {
			end := i + len(tarball)/4
			if end > len(tarball) {
				end = len(tarball)
			}
			pw.Write(tarball[i:end])
			time.Sleep(150 * time.Millisecond)
		}
		pw.Close()
	}()
	if code, body, _ := sendLayer(t, srv, tok, pr, id); code != http.StatusOK {
		t.Fatalf("slow but steady layer = %d %s", code, body)
	}
}

// The idle deadline ends with the body: unpack and mksquashfs may take far
// longer than the idle timeout.
func TestCreateLayerSlowSquashOutlivesIdleTimeout(t *testing.T) {
	s, tok := buildServer(t)
	s.idleTimeout = 200 * time.Millisecond
	s.squash = func(src, dst string) error {
		time.Sleep(800 * time.Millisecond)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}
	srv := stallServer(t, s)
	tarball, id := layerTar(t, "slow squash")
	if code, body, _ := sendLayer(t, srv, tok, bytes.NewReader(tarball), id); code != http.StatusOK {
		t.Fatalf("layer with a slow squash = %d %s", code, body)
	}
	if left := tempEntries(t, s); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestCreateLayerIdleTimeoutOffWaitsForBody(t *testing.T) {
	s, tok := buildServer(t)
	s.idleTimeout = 0
	srv := stallServer(t, s)
	conn, br := stalledLayer(t, srv, tok, 100000)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := http.ReadResponse(br, nil); err == nil {
		t.Fatal("with the idle timeout off a stalled layer was answered")
	}
}

// At most MAX_CONCURRENT_UPLOADS layers are received (and on disk) at once.
// Another one waits for a slot for a while, then gets a retryable 503.
func TestCreateLayerLimitsUploadsReceiving(t *testing.T) {
	s, tok := buildServer(t)
	s.idleTimeout = time.Minute
	s.uploads = make(chan struct{}, 2)
	s.slotWait = time.Second
	s.squash = func(src, dst string) error { return ioutil.WriteFile(dst, []byte("squashfs"), 0644) }
	srv := stallServer(t, s)

	conn1, _ := stalledLayer(t, srv, tok, 100000)
	stalledLayer(t, srv, tok, 100000)
	waitFor(t, "two layers receiving", func() bool { return len(tempEntries(t, s)) == 2 })

	tarball, id := layerTar(t, "third")
	start := time.Now()
	code, body, header := sendLayer(t, srv, tok, bytes.NewReader(tarball), id)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("third layer while two are receiving = %d %s, want 503", code, body)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 4*time.Second {
		t.Fatalf("third layer was refused after %s, want after the slot wait", d)
	}
	if header.Get("Retry-After") == "" || !strings.Contains(body, "MAX_CONCURRENT_UPLOADS") || !strings.Contains(body, `"retry":true`) {
		t.Fatalf("503 is not retryable or does not name the setting: %v %s", header, body)
	}
	if n := len(tempEntries(t, s)); n != 2 {
		t.Fatalf("%d layers on disk, want 2", n)
	}

	// a layer that finds a slot within the wait goes through
	done := make(chan int, 1)
	go func() {
		code, _, _ := sendLayer(t, srv, tok, bytes.NewReader(tarball), id)
		done <- code
	}()
	time.Sleep(200 * time.Millisecond)
	conn1.Close()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("layer after a slot freed = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting layer never got the freed slot")
	}
	waitFor(t, "one layer receiving", func() bool { return len(tempEntries(t, s)) == 1 })
}

// fakeDisk is the free space of the temp filesystem: the base value minus
// what the server wrote there.
type fakeDisk struct {
	mu   sync.Mutex
	free int64
	dir  string
	// low, once set, is the free space reported from then on
	low int64
}

func (d *fakeDisk) freeSpace(dir string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.low > 0 {
		return d.low, nil
	}
	var used int64
	filepathWalk(dir, func(size int64) { used += size })
	return d.free - used, nil
}

func filepathWalk(dir string, f func(int64)) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := dir + "/" + e.Name()
		if e.IsDir() {
			filepathWalk(p, f)
		} else if info, err := e.Info(); err == nil {
			f(info.Size())
		}
	}
}

// A layer the temp disk cannot hold (its tar plus the unpacked copy, plus
// MIN_FREE_DISK) is refused before any of it is written: 507 when it does
// not fit at all, a retryable 503 when it fits once the layers in progress
// are done.
func TestCreateLayerRefusesWhenDiskIsShort(t *testing.T) {
	s, tok := buildServer(t)
	s.idleTimeout = time.Minute
	disk := &fakeDisk{free: 10000}
	s.disk.free = disk.freeSpace
	s.disk.minFree = 1000
	var squashed int32
	s.squash = func(src, dst string) error {
		atomic.AddInt32(&squashed, 1)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}
	srv := stallServer(t, s)

	// 2 x 5000 + 1000 does not fit in 10000
	conn, br := stalledLayer(t, srv, tok, 5000)
	res, body := readAnswer(t, conn, br, 5*time.Second)
	if res.StatusCode != http.StatusInsufficientStorage {
		t.Fatalf("layer larger than the free disk = %d %s, want 507", res.StatusCode, body)
	}
	if !strings.Contains(body, "MIN_FREE_DISK") {
		t.Fatalf("507 does not name the setting: %s", body)
	}
	if left := tempEntries(t, s); len(left) != 0 {
		t.Fatalf("a refused layer wrote %v", left)
	}

	// 2 x 4000 + 1000 fits alone; a second one while the first is
	// receiving does not
	disk.mu.Lock()
	disk.free = 12000
	disk.mu.Unlock()
	conn1, _ := stalledLayer(t, srv, tok, 4000)
	waitFor(t, "the first layer receiving", func() bool { return len(tempEntries(t, s)) == 1 })
	conn2, br2 := stalledLayer(t, srv, tok, 4000)
	res, body = readAnswer(t, conn2, br2, 5*time.Second)
	if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
		t.Fatalf("layer that fits once others finish = %d %v %s, want 503 with Retry-After", res.StatusCode, res.Header, body)
	}
	if !strings.Contains(body, "MIN_FREE_DISK") || !strings.Contains(body, `"retry":true`) {
		t.Fatalf("503 does not name the setting or is not retryable: %s", body)
	}

	// once the first layer is gone, a small one goes through
	conn1.Close()
	waitFor(t, "the first layer's temp files to go", func() bool { return len(tempEntries(t, s)) == 0 && s.disk.outstandingBytes() == 0 })
	tarball, id := layerTar(t, "fits")
	if code, body, _ := sendLayer(t, srv, tok, bytes.NewReader(tarball), id); code != http.StatusOK {
		t.Fatalf("small layer = %d %s", code, body)
	}
	if left := tempEntries(t, s); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if n := s.disk.outstandingBytes(); n != 0 {
		t.Fatalf("%d bytes still reserved", n)
	}
}

// A layer sent without a length (older clients) cannot be checked up front.
// While it is written, the free space is checked; once it falls under
// MIN_FREE_DISK the layer is refused with 507 and its temp file removed.
func TestCreateLayerChunkedStopsWhenDiskRunsLow(t *testing.T) {
	s, tok := buildServer(t)
	disk := &fakeDisk{free: 1 << 30}
	s.disk.free = disk.freeSpace
	s.disk.minFree = 1000
	s.disk.checkEvery = 1024
	var squashed int32
	s.squash = func(src, dst string) error {
		atomic.AddInt32(&squashed, 1)
		return ioutil.WriteFile(dst, []byte("squashfs"), 0644)
	}
	srv := stallServer(t, s)

	tarball, id := layerTar(t, strings.Repeat("y", 64<<10))
	pr, pw := io.Pipe()
	go func() {
		pw.Write(tarball[:8192])
		disk.mu.Lock()
		disk.low = 500
		disk.mu.Unlock()
		pw.Write(tarball[8192:])
		pw.Close()
	}()
	code, body, _ := sendLayer(t, srv, tok, onlyReader{pr}, id)
	if code != http.StatusInsufficientStorage || !strings.Contains(body, "MIN_FREE_DISK") {
		t.Fatalf("chunked layer on a filling disk = %d %s, want 507 naming MIN_FREE_DISK", code, body)
	}
	if left := tempEntries(t, s); len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
	if n := atomic.LoadInt32(&squashed); n != 0 {
		t.Fatal("the refused layer was squashed")
	}

	// with room, a chunked layer goes through
	disk.mu.Lock()
	disk.low = 0
	disk.mu.Unlock()
	if code, body, _ := sendLayer(t, srv, tok, onlyReader{bytes.NewReader(tarball)}, id); code != http.StatusOK {
		t.Fatalf("chunked layer = %d %s", code, body)
	}
}

// Every way a layer upload can fail leaves nothing in the temp directory and
// no slot or reservation held.
func TestCreateLayerFailuresRemoveTempFiles(t *testing.T) {
	s, tok := buildServer(t)
	s.maxLayerSize = 1 << 20
	s.disk.free = (&fakeDisk{free: 1 << 30}).freeSpace
	s.disk.minFree = 1000
	srv := stallServer(t, s)

	tarball, id := layerTar(t, "content")
	check := func(name string, wantCode int, code int, body string) {
		t.Helper()
		if code != wantCode {
			t.Fatalf("%s = %d %s, want %d", name, code, body, wantCode)
		}
		waitFor(t, name+" cleanup", func() bool {
			return len(tempEntries(t, s)) == 0 && len(s.uploads) == 0 && s.disk.outstandingBytes() == 0
		})
	}

	code, body, _ := sendLayer(t, srv, tok, bytes.NewReader(tarball), strings.Repeat("0", 64))
	check("sha mismatch", http.StatusInternalServerError, code, body)

	big, bigID := layerTar(t, strings.Repeat("z", 2<<20))
	code, body, _ = sendLayer(t, srv, tok, onlyReader{bytes.NewReader(big)}, bigID)
	check("oversize chunked", http.StatusRequestEntityTooLarge, code, body)

	s.squash = func(src, dst string) error { return fmt.Errorf("mksquashfs failed") }
	code, body, _ = sendLayer(t, srv, tok, bytes.NewReader(tarball), id)
	check("squash failure", http.StatusInternalServerError, code, body)

	// a client that goes away mid-body
	conn, _ := stalledLayer(t, srv, tok, 100000)
	waitFor(t, "the aborted layer receiving", func() bool { return len(tempEntries(t, s)) == 1 })
	conn.Close()
	waitFor(t, "aborted layer cleanup", func() bool {
		return len(tempEntries(t, s)) == 0 && len(s.uploads) == 0 && s.disk.outstandingBytes() == 0
	})
}

func TestUploadSettings(t *testing.T) {
	for k, v := range map[string]string{"UPLOAD_IDLE_TIMEOUT": "", "MAX_CONCURRENT_UPLOADS": "", "MIN_FREE_DISK": ""} {
		t.Setenv(k, v)
	}
	s := newServer(nil, nil)
	if err := s.configureUploads(); err != nil {
		t.Fatal(err)
	}
	if s.idleTimeout != defaultUploadIdleTimeout || cap(s.uploads) != defaultMaxConcurrentUploads || s.disk.minFree != defaultMinFreeDisk {
		t.Fatalf("defaults: idle %s uploads %d min free %d", s.idleTimeout, cap(s.uploads), s.disk.minFree)
	}
	t.Setenv("UPLOAD_IDLE_TIMEOUT", "off")
	t.Setenv("MAX_CONCURRENT_UPLOADS", "0")
	t.Setenv("MIN_FREE_DISK", "0")
	if err := s.configureUploads(); err != nil {
		t.Fatal(err)
	}
	if s.idleTimeout != 0 || s.uploads != nil || s.disk.minFree != 0 {
		t.Fatalf("off: idle %s uploads %v min free %d", s.idleTimeout, s.uploads, s.disk.minFree)
	}
	t.Setenv("UPLOAD_IDLE_TIMEOUT", "90")
	t.Setenv("MAX_CONCURRENT_UPLOADS", "3")
	t.Setenv("MIN_FREE_DISK", "2g")
	if err := s.configureUploads(); err != nil {
		t.Fatal(err)
	}
	if s.idleTimeout != 90*time.Second || cap(s.uploads) != 3 || s.disk.minFree != 2<<30 {
		t.Fatalf("set: idle %s uploads %d min free %d", s.idleTimeout, cap(s.uploads), s.disk.minFree)
	}
	t.Setenv("UPLOAD_IDLE_TIMEOUT", "soon")
	if err := s.configureUploads(); err == nil || !strings.Contains(err.Error(), "UPLOAD_IDLE_TIMEOUT") {
		t.Fatalf("bad setting error = %v", err)
	}
}
