package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httpclient"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/sirenia/state"
)

// ProcessIDKey returns the discoverd Meta key holding appliance-level peer
// identity for a sirenia process type.
func ProcessIDKey(processType string) string {
	switch processType {
	case "postgres":
		return "POSTGRES_ID"
	case "mariadb":
		return "MARIADB_ID"
	case "mongodb":
		return "MONGODB_ID"
	default:
		return ""
	}
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

var httpClient = &http.Client{
	Timeout:   3 * time.Minute, // client operation timeout
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
	return c.c.Post("/stop", nil, nil)
}

func (c *Client) WaitForReplSync(downstream *discoverd.Instance, idKey string, timeout time.Duration) error {
	return c.waitFor(SyncedWith(downstream, idKey), timeout)
}

// SyncedWith returns a predicate that reports whether replication has caught up
// with expected. When idKey is set, appliance Meta identity is compared instead
// of discoverd Instance.ID so replacements at the same address are detected
// correctly.
func SyncedWith(expected *discoverd.Instance, idKey string) func(*Status) bool {
	return func(status *Status) bool {
		if status.Database == nil || status.Database.SyncedDownstream == nil {
			return false
		}
		synced := status.Database.SyncedDownstream
		if idKey != "" && expected != nil && expected.Meta != nil && synced.Meta != nil {
			id := expected.Meta[idKey]
			return id != "" && id == synced.Meta[idKey]
		}
		return synced.ID == expected.ID
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
