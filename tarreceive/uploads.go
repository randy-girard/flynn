package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	units "github.com/docker/go-units"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// Defaults for the upload settings (EDGE-36). Operators change them with
// `flynn -a tarreceive env:set`.
const (
	defaultUploadIdleTimeout    = uploadlimit.DefaultIdleTimeout
	defaultMaxConcurrentUploads = 8
	defaultMinFreeDisk          = 1 << 30
	// defaultSlotWait is how long a layer waits for an upload slot before
	// it is refused with a retryable 503.
	defaultSlotWait = 30 * time.Second
	// diskCheckEvery is how many bytes of a layer are written between
	// checks of the free space on the temp disk.
	diskCheckEvery = 64 << 20
)

// configureUploads reads UPLOAD_IDLE_TIMEOUT, MAX_CONCURRENT_UPLOADS and
// MIN_FREE_DISK.
func (s *server) configureUploads() error {
	var err error
	if s.idleTimeout, err = uploadlimit.DurationFromEnv("UPLOAD_IDLE_TIMEOUT", defaultUploadIdleTimeout); err != nil {
		return err
	}
	n, err := uploadlimit.FromEnv("MAX_CONCURRENT_UPLOADS", defaultMaxConcurrentUploads)
	if err != nil {
		return fmt.Errorf("error parsing MAX_CONCURRENT_UPLOADS: %s", err)
	}
	s.uploads = nil
	if n > 0 {
		s.uploads = make(chan struct{}, n)
	}
	if s.disk.minFree, err = uploadlimit.FromEnv("MIN_FREE_DISK", defaultMinFreeDisk); err != nil {
		return fmt.Errorf("error parsing MIN_FREE_DISK: %s", err)
	}
	return nil
}

// wrapHandler is the middleware every tarreceive request goes through.
func wrapHandler(h http.Handler) http.Handler {
	return httphelper.ContextInjector("tarreceive", httphelper.NewRequestLogger(h))
}

// retryAfter is the Retry-After of a 503: a slot or disk space frees up
// when a layer in progress finishes.
const retryAfter = 30 * time.Second

// refuse answers a refusal and closes the connection: the body is not read,
// and the server would otherwise wait for the rest of it before answering.
func refuse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Connection", "close")
	e := httphelper.JSONError{Code: errorCodeFor(status), Message: msg}
	if status == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
		e.Retry = true
	}
	log.Printf("layer refused (%d): %s", status, msg)
	httphelper.JSON(w, status, e)
}

// bodyIdle answers a layer whose body sent nothing for the idle timeout.
func (s *server) bodyIdle(w http.ResponseWriter) {
	refuse(w, http.StatusRequestTimeout, fmt.Sprintf("%s (%s); an operator can change it with `flynn -a tarreceive env:set UPLOAD_IDLE_TIMEOUT=<duration>`",
		uploadlimit.ErrIdle, s.idleTimeout))
}

// uploadSlot takes one of the MAX_CONCURRENT_UPLOADS slots, waiting up to
// slotWait for one. It answers the request itself when it returns false.
func (s *server) uploadSlot(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if s.uploads == nil {
		return func() {}, true
	}
	release := func() { <-s.uploads }
	select {
	case s.uploads <- struct{}{}:
		return release, true
	default:
	}
	t := time.NewTimer(s.slotWait)
	defer t.Stop()
	select {
	case s.uploads <- struct{}{}:
		return release, true
	case <-t.C:
		refuse(w, http.StatusServiceUnavailable, fmt.Sprintf("tarreceive is already receiving %d layers (MAX_CONCURRENT_UPLOADS=%d); try again shortly, or an operator can raise it with `flynn -a tarreceive env:set MAX_CONCURRENT_UPLOADS=<n>`",
			cap(s.uploads), cap(s.uploads)))
	case <-r.Context().Done():
		// the client went away
	}
	return nil, false
}

// errDiskLow fails a layer write once the temp disk is under MIN_FREE_DISK.
var errDiskLow = errors.New("the tarreceive temp disk is below MIN_FREE_DISK")

// diskGuard keeps layers from filling the temp filesystem (EDGE-36). A layer
// with a known length reserves twice that (its tar, then its unpacked files
// next to the tar) and is refused up front when the free space, less what
// the layers in progress have reserved and not yet written, would fall
// under minFree. While any layer is written, the free space is checked
// every checkEvery bytes, which also stops layers sent without a length.
type diskGuard struct {
	minFree    int64
	free       func(dir string) (int64, error)
	checkEvery int64

	mu          sync.Mutex
	outstanding int64
}

// outstandingBytes is what the layers in progress have reserved and not
// written yet.
func (g *diskGuard) outstandingBytes() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.outstanding
}

// reserve makes room for a layer of length bytes (-1 is unknown) in dir. It
// answers the request itself when it returns false.
func (g *diskGuard) reserve(w http.ResponseWriter, dir string, length int64) (*reservation, bool) {
	res := &reservation{g: g, dir: dir}
	if g.minFree <= 0 || g.free == nil {
		return res, true
	}
	var need int64
	if length > 0 {
		need = 2 * length
	}
	free, err := g.free(dir)
	if err != nil {
		// do not refuse layers because statfs failed
		log.Printf("checking the free space of the temp disk: %s", err)
		return res, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case free < need+g.minFree:
		refuse(w, http.StatusInsufficientStorage, fmt.Sprintf("the tarreceive temp disk has %s free; this layer needs %s (twice its size, for the tar and its unpacked files) plus MIN_FREE_DISK=%s; free space on the tarreceive volume, or an operator can change the margin with `flynn -a tarreceive env:set MIN_FREE_DISK=<size>`",
			units.BytesSize(float64(free)), units.BytesSize(float64(need)), units.BytesSize(float64(g.minFree))))
		return nil, false
	case free-g.outstanding < need+g.minFree:
		refuse(w, http.StatusServiceUnavailable, fmt.Sprintf("the tarreceive temp disk has %s free, %s of it reserved by layers in progress; this layer needs %s plus MIN_FREE_DISK=%s; try again shortly",
			units.BytesSize(float64(free)), units.BytesSize(float64(g.outstanding)), units.BytesSize(float64(need)), units.BytesSize(float64(g.minFree))))
		return nil, false
	}
	g.outstanding += need
	res.left = need
	return res, true
}

// low answers a layer stopped by errDiskLow.
func (g *diskGuard) low(w http.ResponseWriter) {
	refuse(w, http.StatusInsufficientStorage, fmt.Sprintf("the tarreceive temp disk fell below MIN_FREE_DISK=%s while the layer was written, and the layer was discarded; free space on the tarreceive volume, or an operator can change the margin with `flynn -a tarreceive env:set MIN_FREE_DISK=<size>`",
		units.BytesSize(float64(g.minFree))))
}

// reservation is the disk space one layer holds.
type reservation struct {
	g   *diskGuard
	dir string
	// left is what the layer reserved and has not written yet
	left int64
	// unchecked is what was written since the last free space check
	unchecked int64
}

// writer counts what is written to w against the reservation and checks
// the free space as it goes.
func (r *reservation) writer(w io.Writer) io.Writer {
	return writerFunc(func(p []byte) (int, error) {
		if err := r.check(int64(len(p))); err != nil {
			return 0, err
		}
		n, err := w.Write(p)
		r.wrote(int64(n))
		return n, err
	})
}

func (r *reservation) check(n int64) error {
	g := r.g
	if g.minFree <= 0 || g.free == nil || g.checkEvery <= 0 {
		return nil
	}
	r.unchecked += n
	if r.unchecked < g.checkEvery {
		return nil
	}
	r.unchecked = 0
	free, err := g.free(r.dir)
	if err != nil {
		return nil
	}
	if free-n < g.minFree {
		return errDiskLow
	}
	return nil
}

func (r *reservation) wrote(n int64) {
	if r.left <= 0 {
		return
	}
	if n > r.left {
		n = r.left
	}
	r.g.mu.Lock()
	r.g.outstanding -= n
	r.g.mu.Unlock()
	r.left -= n
}

// release gives back what the layer reserved and did not write.
func (r *reservation) release() {
	if r == nil {
		return
	}
	r.wrote(r.left)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// freeSpace is the space left for unprivileged writes on the filesystem
// holding dir.
func freeSpace(dir string) (int64, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
