// Package cluster implements a client for the Flynn host service.
package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/stream"
)

// NewClient uses the default discoverd configuration to create a cluster
// client.
func NewClient() *Client {
	return NewClientWithServices(nil)
}

// A ServiceFunc is a function that takes a service name and returns
// a discoverd.Service.
type ServiceFunc func(name string) discoverd.Service

// NewClientWithServices uses the provided services to find cluster members. If
// services is nil, the default discoverd client is used.
func NewClientWithServices(services ServiceFunc) *Client {
	return NewClientWithHTTP(services, httphelper.RetryClient)
}

func NewClientWithHTTP(services ServiceFunc, hc *http.Client) *Client {
	return newClient(services, hc)
}

// ErrNotFound is returned when a resource is not found (HTTP status 404).
var ErrNotFound = errors.New("cluster: resource not found")

const (
	hostsDiscoverdRetryAttempts = 45
	hostsDiscoverdRetryInterval = time.Second
)

func newClient(services ServiceFunc, hc *http.Client) *Client {
	if services == nil {
		services = discoverd.NewService
	}
	s := services("flynn-host")
	return &Client{s: s, h: hc}
}

// A Client is used to discover members of the flynn-host cluster.
type Client struct {
	s discoverd.Service
	h *http.Client
}

// Host returns the host identified by id.
func (c *Client) Host(id string) (*Host, error) {
	hosts, err := c.Hosts()
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.ID() == id {
			return h, nil
		}
	}
	return nil, fmt.Errorf("cluster: unknown host %q", id)
}

// Hosts returns a list of hosts in the cluster.
// It retries transient discoverd errors where the flynn-host service exists
// but has not been created yet or returns object_not_found (common right
// after discoverd restarts before each host daemon re-registers).
func (c *Client) Hosts() ([]*Host, error) {
	var (
		insts   []*discoverd.Instance
		lastErr error
	)

	for attempt := 0; attempt < hostsDiscoverdRetryAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(hostsDiscoverdRetryInterval)
		}
		var err error
		insts, err = c.s.Instances()
		if err == nil {
			hosts := make([]*Host, 0, len(insts))
			for _, inst := range insts {
				hosts = append(hosts, NewHost(
					inst.Meta["id"],
					inst.Addr,
					c.h,
					HostTagsFromMeta(inst.Meta),
				))
			}
			return UniqueHosts(hosts), nil
		}
		lastErr = err
		if !httphelper.IsObjectNotFoundError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("timed out waiting for flynn-host service in discoverd: %w", lastErr)
}

// UniqueHosts keeps one client per host ID. flynn-host registers both
// tcp and http discoverd instances; treating each as a distinct host
// made the updater replace infra jobs twice and abort on the first
// StopJob timeout. Prefer the :1113 host API when both are present.
func UniqueHosts(hosts []*Host) []*Host {
	byID := make(map[string]*Host, len(hosts))
	out := make([]*Host, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		id := h.ID()
		if id == "" {
			out = append(out, h)
			continue
		}
		if prev, ok := byID[id]; ok {
			if shouldReplaceHost(prev, h) {
				for i, existing := range out {
					if existing == prev {
						out[i] = h
						break
					}
				}
				byID[id] = h
			}
			continue
		}
		byID[id] = h
		out = append(out, h)
	}
	return out
}

func shouldReplaceHost(prev, next *Host) bool {
	return !isHostAPIAddr(prev.Addr()) && isHostAPIAddr(next.Addr())
}

func isHostAPIAddr(addr string) bool {
	hostport := addr
	if i := strings.Index(addr, "://"); i >= 0 {
		hostport = addr[i+3:]
	}
	if i := strings.Index(hostport, "/"); i >= 0 {
		hostport = hostport[:i]
	}
	return strings.HasSuffix(hostport, ":1113")
}

func HostTagsFromMeta(meta map[string]string) map[string]string {
	tags := make(map[string]string, len(meta))
	for k, v := range meta {
		if strings.HasPrefix(k, host.TagPrefix) {
			tags[strings.TrimPrefix(k, host.TagPrefix)] = v
		}
	}
	return tags
}

func (c *Client) StreamHostEvents(ch chan *discoverd.Event) (stream.Stream, error) {
	return c.s.Watch(ch)
}
