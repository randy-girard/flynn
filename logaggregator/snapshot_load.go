package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/logaggregator/client"
	"github.com/randy-girard/flynn/logaggregator/snapshot"
	"github.com/randy-girard/flynn/logaggregator/utils"
)

// A starting instance reads the buffered logs of the running instances
// (EDGE-31). A deploy stops the old jobs as soon as the new ones are up, so
// the leader can go away mid-transfer, and a connection whose peer vanished
// without a reset can hang a read for minutes. The load is therefore
// bounded, tries the other instances when one fails, and only adopts host
// cursors from a snapshot it read to the end: the lines of a partial one are
// kept, but with no cursor the hosts resend what they have on disk (the
// buffer drops the duplicates) instead of skipping what was never read.

const (
	// defaultSnapshotIdle is how long a snapshot read may go without a byte.
	defaultSnapshotIdle = 10 * time.Second
	// defaultSnapshotSource bounds one source's whole transfer.
	defaultSnapshotSource = 60 * time.Second
	// defaultSnapshotTotal bounds the load over all sources.
	defaultSnapshotTotal = 90 * time.Second
	// defaultSnapshotDial bounds connecting to a source (and twice it the
	// wait for response headers).
	defaultSnapshotDial = 5 * time.Second
	// defaultSnapshotDrain is how long a stopping instance keeps writing the
	// snapshots new instances asked it for.
	defaultSnapshotDrain = 10 * time.Second
)

// snapshotCountTrailer is the HTTP trailer in which a snapshot response says
// how many messages it sent. A response without it is from a release before
// EDGE-31; one with it and a different count is incomplete.
const snapshotCountTrailer = "Flynn-Snapshot-Count"

// snapshotLoader says where a starting instance reads the buffered logs of
// the running ones from.
type snapshotLoader struct {
	// Sources are logaggregator API base URLs, tried in order (leader first).
	Sources []string
	Key     string
	HTTP    *http.Client

	IdleTimeout   time.Duration
	SourceTimeout time.Duration
	TotalTimeout  time.Duration
}

type snapshotResult struct {
	// Source is the source of a complete snapshot, or the last one tried.
	Source string
	// Messages is how many messages were read from Source.
	Messages int
	Complete bool
}

// snapshotHTTPClient has no retrying dialer and no total timeout (the
// loader's context bounds the transfer), but bounds the dial and the wait
// for response headers.
func snapshotHTTPClient(dialTimeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
			ResponseHeaderTimeout: 2 * dialTimeout,
			DisableKeepAlives:     true,
		},
	}
}

// loadSnapshotFromPeers loads the first complete snapshot of l.Sources. It
// returns within about l.TotalTimeout, and never applies the host cursors
// of a snapshot it did not read to the end.
func (s *Server) loadSnapshotFromPeers(l snapshotLoader) snapshotResult {
	ctx, cancel := context.WithTimeout(context.Background(), l.TotalTimeout)
	defer cancel()

	var res snapshotResult
	for _, src := range l.Sources {
		if ctx.Err() != nil {
			log15.Error("snapshot load out of time, not trying the other instances", "timeout", l.TotalTimeout)
			break
		}
		start := time.Now()
		n, cursors, err := s.readSnapshotFrom(ctx, l, src)
		res = snapshotResult{Source: src, Messages: n}
		if err != nil {
			log15.Error("error receiving snapshot, keeping the lines read but not its cursors", "source", src, "messages", n, "duration", time.Since(start), "error", err)
			continue
		}
		for host, c := range cursors {
			s.Cursors.Update(host, c)
		}
		res.Complete = true
		log15.Info("loaded snapshot", "source", src, "messages", n, "duration", time.Since(start))
		return res
	}
	if len(l.Sources) > 0 {
		log15.Error("starting without a complete snapshot, hosts resend the logs they have", "tried", len(l.Sources))
	}
	return res
}

// readSnapshotFrom feeds the messages of src's snapshot as they arrive and
// returns how many it read and their host cursors. An error means the
// snapshot was not read to the end: a stall longer than l.IdleTimeout, a
// transfer over l.SourceTimeout, a connection cut mid-record, or fewer
// messages than the count trailer.
func (s *Server) readSnapshotFrom(ctx context.Context, l snapshotLoader, src string) (int, map[string]*utils.HostCursor, error) {
	ctx, cancel := context.WithTimeout(ctx, l.SourceTimeout)
	defer cancel()

	c, err := client.NewWithHTTP(strings.TrimSuffix(src, "/"), l.HTTP)
	if err != nil {
		return 0, nil, err
	}
	c.Key = l.Key
	res, err := c.GetSnapshotWithContext(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()

	body := newIdleReader(res.Body, l.IdleTimeout, cancel)
	defer body.stop()

	cursors := make(map[string]*utils.HostCursor)
	n := 0
	sc := snapshot.NewScanner(body)
	for sc.Scan() {
		cursor, err := utils.ParseHostCursor(sc.Message)
		if err != nil {
			return n, nil, err
		}
		host := string(sc.Message.Hostname)
		if cur, ok := cursors[host]; !ok || cursor.After(*cur) {
			cursors[host] = cursor
		}
		s.Aggregator.Feed(sc.Message)
		n++
	}
	if err := sc.Err(); err != nil {
		if body.timedOut() {
			return n, nil, fmt.Errorf("no data for %s: %w", l.IdleTimeout, err)
		}
		if ctx.Err() != nil {
			return n, nil, fmt.Errorf("%w: %v", ctx.Err(), err)
		}
		return n, nil, err
	}
	if v := res.Trailer.Get(snapshotCountTrailer); v != "" {
		want, err := strconv.Atoi(v)
		if err != nil {
			return n, nil, fmt.Errorf("bad %s trailer %q", snapshotCountTrailer, v)
		}
		if want != n {
			return n, nil, fmt.Errorf("snapshot incomplete: read %d of %d messages", n, want)
		}
	} else {
		log15.Info("snapshot has no message count (older release), taking its clean end as complete", "source", src)
	}
	return n, cursors, nil
}

// idleReader cancels the request when no Read returns data for timeout.
type idleReader struct {
	r       io.Reader
	timeout time.Duration
	timer   *time.Timer

	mtx  sync.Mutex
	idle bool
}

func newIdleReader(r io.Reader, timeout time.Duration, cancel func()) *idleReader {
	ir := &idleReader{r: r, timeout: timeout}
	ir.timer = time.AfterFunc(timeout, func() {
		ir.mtx.Lock()
		ir.idle = true
		ir.mtx.Unlock()
		cancel()
	})
	return ir
}

func (ir *idleReader) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.timer.Reset(ir.timeout)
	}
	return n, err
}

func (ir *idleReader) stop() { ir.timer.Stop() }

func (ir *idleReader) timedOut() bool {
	ir.mtx.Lock()
	defer ir.mtx.Unlock()
	return ir.idle
}

// errSnapshotDrain is returned when snapshot responses are still being
// written when the drain time is up.
var errSnapshotDrain = errors.New("snapshot responses still being written")

// drain waits up to timeout for the snapshot responses being written, so new
// instances that asked before this one stopped get all of it.
func (t *snapshotTracker) drain(timeout time.Duration) error {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errSnapshotDrain
	}
}
