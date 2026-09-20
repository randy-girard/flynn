package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

// ProcessIDKey returns the discoverd Meta key holding appliance-level peer
// identity for a sirenia process type.
func ProcessIDKey(processType string) string {
	if processType == "" {
		return ""
	}
	return strings.ToUpper(processType) + "_ID"
}

// SamePeer reports whether a and b are the same sirenia appliance peer.
// When idKey is set (POSTGRES_ID / MARIADB_ID / MONGODB_ID), identity is the
// appliance meta value so a replacement at a new address still matches. An
// empty idKey or missing meta falls back to discoverd Instance.ID.
func SamePeer(idKey string, a, b *discoverd.Instance) bool {
	if a == nil || b == nil {
		return false
	}
	if idKey != "" && a.Meta != nil && b.Meta != nil && a.Meta[idKey] != "" {
		return a.Meta[idKey] == b.Meta[idKey]
	}
	return a.ID == b.ID
}

type DatabaseInfo struct {
	Config           *state.Config       `json:"config"`
	Running          bool                `json:"running"`
	SyncedDownstream *discoverd.Instance `json:"synced_downstream"`
	XLog             string              `json:"xlog"`
	UserExists       bool                `json:"user_exists"`
	ReadWrite        bool                `json:"read_write"`
}

type Status struct {
	Peer     *state.PeerInfo `json:"peer"`
	Database *DatabaseInfo   `json:"database"`
}

type Client struct {
	c *httpclient.Client
}

// httpClient is used for sirenia /status polls. WaitFor* already retries
// until its own deadline; a 15-minute Client.Timeout meant one hung /status
// (no ResponseHeaderTimeout on the shared transport) consumed the entire
// WaitForReplSync budget and surfaced as "timeout waiting for expected status".
var httpClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: httphelper.RetryClient.Transport,
}

// stopHTTPClient uses a longer timeout than status polling because graceful
// database shutdown (mysqld/postgres) can take several minutes.
var stopHTTPClient = &http.Client{
	Timeout:   10 * time.Minute,
	Transport: httphelper.RetryClient.Transport,
}

func NewClient(addr string) *Client {
	return NewClientWithHTTP(addr, httpClient)
}

func NewClientWithHTTP(addr string, httpClient *http.Client) *Client {
	// remove port, if any
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)

	return &Client{
		c: &httpclient.Client{
			URL:  fmt.Sprintf("http://%s:%d", host, port+1),
			HTTP: httpClient,
		},
	}
}

func (c *Client) Status() (*Status, error) {
	res := &Status{}
	return res, c.c.Get("/status", res)
}

func (c *Client) Stop() error {
	stopClient := &httpclient.Client{
		URL:  c.c.URL,
		Key:  c.c.Key,
		HTTP: stopHTTPClient,
	}
	return stopClient.Post("/stop", nil, nil)
}

// IsRecoverableStopError reports whether a failed Stop request may still have
// started peer shutdown and the caller should wait for discoverd to report the
// instance as down rather than failing the deploy immediately.
func IsRecoverableStopError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "Client.Timeout exceeded") ||
		strings.Contains(msg, "timeout awaiting headers")
}

// IsPeerUnreachableError reports whether a request to a sirenia peer's HTTP API
// failed because the peer could not be contacted at all: its job is down or the
// host is unreachable, so the TCP connection was never established. Such a peer
// is effectively already stopped, so callers should wait for discoverd to
// report it down rather than aborting the deploy.
func IsPeerUnreachableError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no route to host") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "connection reset by peer") {
		return true
	}
	// A dial timeout (as opposed to a mid-request read timeout) means the TCP
	// connection could not be established within the dialer's retry budget.
	return strings.Contains(msg, "dial ") && strings.Contains(msg, "i/o timeout")
}

func (c *Client) WaitForReplSync(downstream *discoverd.Instance, idKey string, timeout time.Duration) error {
	if downstream == nil {
		return fmt.Errorf("nil downstream peer")
	}
	dclient := NewClient(downstream.Addr)
	return c.WaitUntil(func(up *Status) bool {
		return replSyncCaughtUp(up, dclient, downstream, idKey)
	}, timeout)
}

// WaitUntil polls /status until pred returns true or timeout elapses.
func (c *Client) WaitUntil(pred func(*Status) bool, timeout time.Duration) error {
	return c.waitFor(pred, timeout)
}

// IsTailAsync reports whether peer is the last entry in the cluster's async
// chain. A newly started replacement must land here: if it were inserted at
// the front, the existing first async would re-point its upstream at a peer
// that has not finished its base backup.
func IsTailAsync(s *state.State, peer *discoverd.Instance, idKey string) bool {
	if s == nil || peer == nil || len(s.Async) == 0 {
		return false
	}
	return SamePeer(idKey, s.Async[len(s.Async)-1], peer)
}

// SyncReplaceTopologyReady is true when the cluster has absorbed a newly
// started sync-replacement as the tail async without taking over the recorded
// sync or shuffling the first async. The deploy worker waits for this before
// stopping the old sync, so the replica set keeps three live peers while the
// new job completes its base backup.
func SyncReplaceTopologyReady(oldSync, firstAsync, newPeer *discoverd.Instance, idKey string) func(*Status) bool {
	return func(status *Status) bool {
		if status == nil || status.Peer == nil || status.Peer.State == nil {
			return false
		}
		s := status.Peer.State
		if !SamePeer(idKey, s.Sync, oldSync) {
			return false
		}
		if len(s.Async) < 2 {
			return false
		}
		if firstAsync != nil && !SamePeer(idKey, s.Async[0], firstAsync) {
			return false
		}
		return IsTailAsync(s, newPeer, idKey)
	}
}

// replSyncCaughtUp reports whether upstream names expected as its synced
// replica and that peer is actually running. An upstream can report
// SyncedDownstream from a stale pg_stat_replication row while the replacement
// job is still waiting for a basebackup; requiring Database.Running on both
// sides prevents the sirenia deploy from stopping the next peer too early.
func replSyncCaughtUp(up *Status, downstreamClient *Client, expected *discoverd.Instance, idKey string) bool {
	if up == nil || up.Database == nil || !up.Database.Running || up.Database.SyncedDownstream == nil {
		return false
	}
	if !SamePeer(idKey, expected, up.Database.SyncedDownstream) {
		return false
	}
	if downstreamClient == nil {
		return false
	}
	down, err := downstreamClient.Status()
	if err != nil || down == nil || down.Database == nil || !down.Database.Running {
		return false
	}
	if !downstreamFollowsUpstream(up, down, idKey) {
		return false
	}
	return true
}

// downstreamFollowsUpstream reports whether down is configured to replicate
// from up. After an HA rolling deploy stops the old sync, the replacement
// async must re-point at the primary before it is a valid cascading source
// for the next peer; matching only SyncedDownstream allowed a stale primary
// sync name to pass while the async still followed the peer that was just
// stopped.
func downstreamFollowsUpstream(up, down *Status, idKey string) bool {
	if down == nil || down.Database == nil || down.Database.Config == nil || down.Database.Config.Upstream == nil {
		return false
	}
	if up == nil || up.Peer == nil || up.Peer.ID == "" {
		return true
	}
	src := down.Database.Config.Upstream
	if idKey != "" && src.Meta != nil && src.Meta[idKey] != "" {
		return src.Meta[idKey] == up.Peer.ID
	}
	return src.ID == up.Peer.ID
}

// SyncedWith returns a predicate that reports whether replication has caught up
// with expected. When idKey is set, appliance Meta identity is compared instead
// of discoverd Instance.ID so replacements at the same address are detected
// correctly.
func SyncedWith(expected *discoverd.Instance, idKey string) func(*Status) bool {
	return func(status *Status) bool {
		if status == nil || status.Database == nil || !status.Database.Running || status.Database.SyncedDownstream == nil {
			return false
		}
		synced := status.Database.SyncedDownstream
		return SamePeer(idKey, expected, synced)
	}
}

func (c *Client) WaitForReadWrite(timeout time.Duration) error {
	return c.waitFor(func(status *Status) bool {
		return status.Database != nil && status.Database.ReadWrite
	}, timeout)
}

var ErrTimeout = errors.New("timeout waiting for expected status")

func (c *Client) waitFor(expected func(*Status) bool, timeout time.Duration) error {
	start := time.Now()
	for {
		status, err := c.Status()
		if err != nil {
			if !isNetError(err) {
				return err
			}
		} else if expected(status) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
		if time.Now().Sub(start) > timeout {
			return ErrTimeout
		}
	}
}

func isNetError(err error) bool {
	switch err.(type) {
	case *net.OpError:
		return true
	case *url.Error:
		return true
	}
	if err == io.EOF {
		return true
	}
	return false
}
