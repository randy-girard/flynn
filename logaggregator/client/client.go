package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/appliancehttp"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// ErrNotFound is returned when a resource is not found (HTTP status 404).
var ErrNotFound = errors.New("logaggregator: resource not found")

type Client struct {
	*httpclient.Client
}

// newClient creates a generic Client object, additional attributes must
// be set by the caller
func newClient(url string, http *http.Client) *Client {
	return &Client{
		Client: &httpclient.Client{
			ErrNotFound: ErrNotFound,
			URL:         url,
			HTTP:        http,
		},
	}
}

// New creates a Client pointing at uri. It sends the cluster key from the
// process environment (CONTROLLER_KEY, or the first AUTH_KEY entry), which
// every API route except the health check requires.
func New(uri string) (*Client, error) {
	return NewWithHTTP(uri, httphelper.RetryClient)
}

// NewWithKey is New with an explicit cluster key, for callers (flynn-host)
// that resolve the key themselves.
func NewWithKey(uri, key string) (*Client, error) {
	c, err := NewWithHTTP(uri, httphelper.RetryClient)
	if err != nil {
		return nil, err
	}
	c.Key = key
	return c, nil
}

// NewWithHTTP is New with the specified http client.
func NewWithHTTP(uri string, httpClient *http.Client) (*Client, error) {
	if uri == "" {
		uri = "http://logaggregator.discoverd"
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	c := newClient(u.String(), httpClient)
	c.Key = appliancehttp.ClientKey()
	return c, nil
}

// GetLog returns a ReadCloser log stream of the log channel with ID channelID.
// Each line returned will be a JSON serialized Message.
//
// If lines is above zero, the number of lines returned will be capped at that
// value. Otherwise, all available logs are returned. If follow is true, new log
// lines are streamed after the buffered log.
func (c *Client) GetLog(channelID string, opts *logagg.LogOpts) (io.ReadCloser, error) {
	path := fmt.Sprintf("/log/%s", channelID)
	if opts != nil {
		if encodedQuery := opts.EncodedQuery(); encodedQuery != "" {
			path = fmt.Sprintf("%s?%s", path, encodedQuery)
		}
	}
	res, err := c.RawReq("GET", path, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

// Message represents a single log message.
type Message struct {
	// Hostname is the host that the job was running on when this log message was
	// emitted.
	HostID string `json:"host_id,omitempty"`
	// JobID is the ID of the job that emitted this log message.
	JobID string `json:"job_id,omitempty"`
	// JobName is the allocated short process name (web.1), when present.
	JobName string `json:"job_name,omitempty"`
	// Msg is the actual content of this log message.
	Msg string `json:"msg,omitempty"`
	// ProcessType is the type of process that emitted this log message.
	ProcessType string `json:"process_type,omitempty"`
	// Source is the source of this log message, such as "app" or "router".
	Source string `json:"source,omitempty"`
	// Stream is the I/O stream that emitted this message, such as "stdout" or
	// "stderr".
	Stream logagg.StreamType `json:"stream,omitempty"`
	// Timestamp is the time that this log line was emitted.
	Timestamp time.Time `json:"timestamp,omitempty"`
}

func (c *Client) GetCursors() (map[string]utils.HostCursor, error) {
	var res map[string]utils.HostCursor
	return res, c.Get("/cursors", &res)
}

// GetSnapshotWithContext is GetSnapshot returning the whole response, so the
// caller can read its Flynn-Snapshot-Count trailer once the body is read.
// ctx bounds the request and the body read.
func (c *Client) GetSnapshotWithContext(ctx context.Context) (*http.Response, error) {
	return c.RawReqWithContext(ctx, "GET", "/snapshot", nil, nil, nil)
}

func (c *Client) GetSnapshot() (io.ReadCloser, error) {
	res, err := c.RawReq("GET", "/snapshot", nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
