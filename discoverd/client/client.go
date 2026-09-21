package discoverd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inconshreveable/log15"
	dt "github.com/randy-girard/flynn/discoverd/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/stream"
)

const (
	requestDeadline = 10 * time.Second
	retryInterval   = 100 * time.Millisecond
)

var ErrTimedOut = errors.New("discoverd: timed out waiting for instances")

var defaultLogger = log15.New("component", "discoverd")

func init() {
	defaultLogger.SetHandler(log15.StreamHandler(os.Stderr, log15.LogfmtFormat()))
}

type Config struct {
	Endpoints []string
	// AuthKey is sent as Auth-Key / HTTP basic password. When empty, the
	// client reads DISCOVERD_AUTH_KEY from the environment at request time so
	// late bootstrap Setenv still authenticates DefaultClient.
	AuthKey string
}

type Client struct {
	servers map[string]*httpclient.Client
	hc      *http.Client
	pinned  string
	leader  string
	idx     uint64
	mu      sync.RWMutex
	Logger  log15.Logger
	Key     string
}

func NewClientWithConfig(config Config) *Client {
	client := &Client{
		servers: make(map[string]*httpclient.Client, len(config.Endpoints)),
		Logger:  defaultLogger,
		Key:     config.AuthKey,
	}
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) > 0 {
			for attr, val := range via[0].Header {
				if _, ok := req.Header[attr]; !ok {
					req.Header[attr] = val
				}
			}
		}
		client.updateLeader(req.URL.Host)
		return nil
	}
	client.hc = &http.Client{
		CheckRedirect: checkRedirect,
		// use a low timeout so the client doesn't hang if any of the
		// discoverd servers become unreachable (requests will be
		// retried on fallback servers if they timeout, see client.Do)
		Timeout: heartbeatInterval,
	}
	for _, e := range config.Endpoints {
		client.servers[e] = client.httpClient(e)
	}
	return client
}

func NewClientWithURL(url string) *Client {
	cfg := defaultConfig()
	cfg.Endpoints = formatURLs(strings.Split(url, ","))
	return NewClientWithConfig(cfg)
}

func NewClient() *Client {
	return NewClientWithConfig(defaultConfig())
}

func defaultConfig() Config {
	urls := os.Getenv("DISCOVERD")
	if urls == "" || urls == "none" {
		urls = "http://127.0.0.1:1111"
	}
	return Config{
		Endpoints: formatURLs(strings.Split(urls, ",")),
		AuthKey:   os.Getenv("DISCOVERD_AUTH_KEY"),
	}
}

func (c *Client) authKey() string {
	if c.Key != "" {
		return c.Key
	}
	return os.Getenv("DISCOVERD_AUTH_KEY")
}

func (c *Client) refreshAuth() {
	key := c.authKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		s.Key = key
	}
}

func (c *Client) authHeader() http.Header {
	h := make(http.Header)
	if key := c.authKey(); key != "" {
		h.Set("Auth-Key", key)
	}
	return h
}

func formatURLs(urls []string) []string {
	formatted := make([]string, 0, len(urls))
	for _, u := range urls {
		if !strings.HasPrefix(u, "http") {
			u = "http://" + u
		}
		formatted = append(formatted, u)
	}
	return formatted
}

func (c *Client) updateLeader(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	url := "http://" + host
	if c.leader == url {
		return
	}
	for addr, s := range c.servers {
		if s.URL == url {
			c.leader = addr
			break
		}
	}
}

func (c *Client) httpClient(url string) *httpclient.Client {
	return &httpclient.Client{
		URL:  url,
		HTTP: c.hc,
		Key:  c.authKey(),
	}
}

// Retrieves current pin value
func (c *Client) currentPin() (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.leader, c.pinned
}

// Update the currently pinned server
func (c *Client) updatePin(new string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = new
}

// Updates the list of peers
func (c *Client) updateServers(servers []string, idx uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If the new index isn't greater than the current index then ignore
	// changes to the peer list. Prevents out of order request handling
	// nuking a more recent version of the peer list.
	if idx < c.idx {
		return
	}
	c.idx = idx
	servers = formatURLs(servers)

	// First add any new servers
	for _, s := range servers {
		if _, ok := c.servers[s]; !ok {
			c.servers[s] = c.httpClient(s)
		}
	}

	// Then remove any that are no longer current
	for addr := range c.servers {
		present := false
		for _, s := range servers {
			if _, ok := c.servers[s]; ok {
				present = true
				break
			}
		}
		if !present {
			delete(c.servers, addr)
		}
	}
}

func (c *Client) Do(method string, path string, in, out interface{}, streamReq bool) (res stream.Stream, err error) {
	c.refreshAuth()

	var leaderReq bool
	switch method {
	case "PUT", "DEL", "POST":
		leaderReq = true
	}

	leader, pinned := c.currentPin()

	// Attempt to direct writes and streaming requests to the last known leader
	if leaderReq {
		pinned = leader
	}

	// Make a copy of the current peers under read lock
	c.mu.RLock()
	servers := make(map[string]*httpclient.Client, len(c.servers))
	for a, s := range c.servers {
		servers[a] = s
	}
	c.mu.RUnlock()

	// Create an ordered list of peers to attempt the request, pinned server first
	orderedServers := make([]*httpclient.Client, 0, len(c.servers))

	// First add the pinned server
	for addr, s := range servers {
		if addr == pinned {
			orderedServers = append(orderedServers, s)
		}
	}

	// Then all other servers, sans the pinned server above.
	for addr, s := range servers {
		if addr != pinned {
			orderedServers = append(orderedServers, s)
		}
	}

	errs := make([]string, 0, len(servers))
	for startTime := time.Now(); time.Since(startTime) < requestDeadline; time.Sleep(retryInterval) {
		for _, hc := range orderedServers {
			var rsp *http.Response
			if streamReq {
				h := c.authHeader()
				h.Set("Accept", "text/event-stream")
				// use a copy of the client with a zero timeout (it doesn't really
				// make sense to have a stream with a timeout)
				httpClient := *hc.HTTP
				httpClient.Timeout = 0
				rsp, err = hc.RawReqWithHTTP(method, path, h, in, nil, &httpClient)
				if err == nil {
					res = httpclient.Stream(rsp, out)
				}
			} else {
				h := c.authHeader()
				h.Set("Accept", "application/json")
				rsp, err = hc.RawReq(method, path, h, in, out)
				if err == nil && out == nil {
					rsp.Body.Close()
				}
			}
			// If we consider the error not to be an issue with the request but rather
			// a transient network/server error then we try again with a different server
			if err != nil && isRetryable(err) {
				errs = append(errs, err.Error())
				continue
			} else if err != nil {
				return nil, err
			}
			// If the pinned server failed to fulfill our request then update the pin
			// We don't update the pin on leader requests
			if hc.Host != pinned && !leaderReq {
				c.updatePin(hc.Host)
			}
			peers := rsp.Header.Get("Discoverd-Current-Peers")
			idx := rsp.Header.Get("Discoverd-Current-Index")
			if i, err := strconv.ParseUint(idx, 10, 64); err == nil && i > 0 && peers != "" {
				c.updateServers(strings.Split(peers, ","), i)
			}
			return res, nil
		}
	}
	return nil, fmt.Errorf("Error sending HTTP request, errors: %s", strings.Join(errs, ","))
}

func isRetryable(err error) bool {
	switch err.(type) {
	case *net.OpError:
		return true
	case *url.Error:
		return true
	}
	if err == io.EOF {
		return true
	}
	return hh.IsRetryableError(err)
}

func (c *Client) Stream(method string, path string, in, out interface{}) (stream.Stream, error) {
	return c.Do(method, path, in, out, true)
}

func (c *Client) Send(method string, path string, in, out interface{}) error {
	_, err := c.Do(method, path, in, out, false)
	return err
}

func (c *Client) Get(path string, out interface{}) error {
	return c.Send("GET", path, nil, out)
}

func (c *Client) Put(path string, in, out interface{}) error {
	return c.Send("PUT", path, in, out)
}

func (c *Client) Delete(path string) error {
	return c.Send("DELETE", path, nil, nil)
}

func (c *Client) Ping(url string) error {
	if s := c.serverByHost(url); s != nil {
		return s.Get("/ping", nil)
	}
	return fmt.Errorf("discoverd server not found in server list")
}

func (c *Client) Shutdown(url string) (res dt.TargetLogIndex, err error) {
	if s := c.serverByHost(url); s != nil {
		return res, s.Post("/shutdown", nil, &res)
	}
	return res, fmt.Errorf("discoverd server not found in server list")
}

func (c *Client) Promote(url string) error {
	if s := c.serverByHost(url); s != nil {
		return s.Post("/raft/promote", nil, nil)
	}
	return fmt.Errorf("discoverd server not found in server list")
}

func (c *Client) Demote(url string) error {
	if s := c.serverByHost(url); s != nil {
		return s.Post("/raft/demote", nil, nil)
	}
	return fmt.Errorf("discoverd server not found in server list")
}

func (c *Client) RaftPeers() (res []string, err error) {
	return res, c.Get("/raft/peers", &res)
}

func (c *Client) RaftAddPeer(addr string) (res dt.TargetLogIndex, err error) {
	return res, c.Put(fmt.Sprintf("/raft/peers/%s", addr), nil, &res)
}

func (c *Client) RaftRemovePeer(addr string) error {
	return c.Delete(fmt.Sprintf("/raft/peers/%s", addr))
}

func (c *Client) RaftLeader() (res dt.RaftLeader, err error) {
	return res, c.Get("/raft/leader", &res)
}

func (c *Client) serverByHost(url string) *httpclient.Client {
	c.refreshAuth()
	for _, s := range c.servers {
		if s.URL == url {
			return s
		}
	}
	return nil
}
