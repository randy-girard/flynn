package discoverd

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/randy-girard/flynn/pkg/attempt"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/random"
)

// MaxHeartbeatBatch is the most instance ids one POST /heartbeats accepts.
const MaxHeartbeatBatch = 4096

// HeartbeatStamp is one id in a leader heartbeat batch. It does not carry
// Addr, Proto, or Meta. Those changes stay on PUT.
type HeartbeatStamp struct {
	Service string `json:"service"`
	ID      string `json:"id"`
}

// HeartbeatBatch is the body of POST /heartbeats.
type HeartbeatBatch struct {
	Instances []HeartbeatStamp `json:"instances"`
}

// HeartbeatBatchResult lists ids the leader does not have. Those are not
// stamped. The client full-PUTs them.
type HeartbeatBatchResult struct {
	Missing []HeartbeatStamp `json:"missing,omitempty"`
}

// errRouteMissing is a plain 404 from a discoverd that has no batch route.
// A nil error here would look like a successful stamp during a rolling upgrade.
var errRouteMissing = errors.New("discoverd: http route not found")

// EnvInstanceMeta are environment variables which will be automatically added
// to instance metadata if present.
var EnvInstanceMeta = map[string]struct{}{
	"FLYNN_APP_ID":       {},
	"FLYNN_APP_NAME":     {},
	"FLYNN_RELEASE_ID":   {},
	"FLYNN_PROCESS_TYPE": {},
	"FLYNN_JOB_ID":       {},
}

type Heartbeater interface {
	SetMeta(map[string]string) error
	Close() error
	Addr() string
	SetClient(*Client)
}

var runAttempts = attempt.Strategy{
	Min:   5,
	Total: 60 * time.Second,
	Delay: 200 * time.Millisecond,
}

func (c *Client) maybeAddService(service string) error {
	err := c.AddService(service, nil)
	if hh.IsObjectExistsError(err) {
		return nil
	}
	return err
}

func (c *Client) AddServiceAndRegister(service, addr string) (Heartbeater, error) {
	if err := c.maybeAddService(service); err != nil {
		return nil, err
	}
	return c.Register(service, addr)
}

func (c *Client) AddServiceAndRegisterInstance(service string, inst *Instance) (Heartbeater, error) {
	if err := c.maybeAddService(service); err != nil {
		return nil, err
	}
	return c.RegisterInstance(service, inst)
}

func (c *Client) Register(service, addr string) (Heartbeater, error) {
	return c.RegisterInstance(service, &Instance{Addr: addr})
}

// HeartbeatOptions controls how a registration is repaired after discoverd
// drops the service. Service is the leader type to recreate; nil means
// oldest, matching AddService(nil). ClaimLeader makes this instance the
// leader again, for a one-node manual service that does not run sirenia.
type HeartbeatOptions struct {
	Service     *ServiceConfig
	ClaimLeader bool
}

func (c *Client) RegisterInstance(service string, inst *Instance) (Heartbeater, error) {
	return c.RegisterInstanceOptions(service, inst, HeartbeatOptions{})
}

func (c *Client) RegisterInstanceOptions(service string, inst *Instance, opts HeartbeatOptions) (Heartbeater, error) {
	inst.Addr = expandAddr(inst.Addr)
	if inst.Proto == "" {
		inst.Proto = "tcp"
	}
	inst.ID = inst.id()
	// add EnvInstanceMeta if present
	for _, env := range os.Environ() {
		kv := strings.SplitN(env, "=", 2)
		if _, ok := EnvInstanceMeta[kv[0]]; !ok {
			continue
		}
		if inst.Meta == nil {
			inst.Meta = make(map[string]string)
		}
		inst.Meta[kv[0]] = kv[1]
	}
	h := newHeartbeater(c, service, inst)
	h.serviceConfig = opts.Service
	h.claimLeader = opts.ClaimLeader
	err := runAttempts.Run(func() error {
		firstErr := make(chan error)
		go h.run(firstErr)
		return <-firstErr
	})
	if err != nil {
		return nil, err
	}

	return h, nil
}

func newHeartbeater(c *Client, service string, inst *Instance) *heartbeater {
	h := &heartbeater{
		service: service,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		inst:    inst.Clone(),
	}
	h.c.Store(c)
	return h
}

type heartbeater struct {
	c atomic.Value // *Client

	stop chan struct{}
	done chan struct{}

	// Mutex protects inst.Meta
	sync.Mutex
	inst *Instance

	service       string
	serviceConfig *ServiceConfig
	claimLeader   bool
	closeOnce     sync.Once
	registered    atomic.Bool
	closing       atomic.Bool
}

func (h *heartbeater) Close() error {
	h.closeOnce.Do(func() {
		h.closing.Store(true)
		close(h.stop)
		<-h.done
	})
	return nil
}

func (h *heartbeater) SetMeta(meta map[string]string) error {
	h.Lock()
	defer h.Unlock()
	h.inst.Meta = meta
	return h.client().Put(fmt.Sprintf("/services/%s/instances/%s", h.service, h.inst.ID), h.inst, nil)
}

func (h *heartbeater) Addr() string {
	return h.inst.Addr
}

func (h *heartbeater) SetClient(c *Client) {
	if c == nil {
		return
	}
	old := h.client()
	h.c.Store(c)
	if !h.registered.Load() || h.closing.Load() {
		return
	}
	if old != nil && old != c {
		old.untrackHeartbeat(h)
	}
	c.trackHeartbeat(h)
}

func (h *heartbeater) client() *Client {
	return h.c.Load().(*Client)
}

const (
	heartbeatInterval        = 5 * time.Second
	heartbeatJitter          = 2 * time.Second
	heartbeatFailingInterval = 200 * time.Millisecond
)

// heartbeatDelay and heartbeatRetryDelay are the flush periods. Tests replace
// them. Production stays on nextHeartbeat, which is interval minus jitter.
var (
	heartbeatDelay      = nextHeartbeat
	heartbeatRetryDelay = nextHeartbeatFailing
	heartbeatBatchLimit = MaxHeartbeatBatch
)

func (h *heartbeater) run(firstErr chan<- error) {
	err := h.registerInstance()
	firstErr <- err
	if err != nil {
		return
	}
	h.registered.Store(true)
	if !h.closing.Load() {
		h.client().trackHeartbeat(h)
	}
	<-h.stop
	h.client().untrackHeartbeat(h)
	h.client().Delete(h.instancePath())
	close(h.done)
}

func (h *heartbeater) instancePath() string {
	h.Lock()
	id := h.inst.ID
	h.Unlock()
	return fmt.Sprintf("/services/%s/instances/%s", h.service, id)
}

// missingService reports a discoverd 404 for the service itself. An instance
// 404 is a different failure and must not create a new service.
func missingService(err error) bool {
	if err == nil || !hh.IsObjectNotFoundError(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "service") && !strings.Contains(msg, "instance")
}

// registerInstance heartbeats this instance. If discoverd no longer has the
// service, it is created again with the leader type from registration, then
// the instance is put back. A one-node manual service that claimed leadership
// at start claims it again so leader.<service>.discoverd resolves.
func (h *heartbeater) registerInstance() error {
	if h.closing.Load() {
		return nil
	}
	h.Lock()
	inst := h.inst
	h.Unlock()
	path := fmt.Sprintf("/services/%s/instances/%s", h.service, inst.ID)
	err := h.client().Put(path, inst, nil)
	if !missingService(err) {
		return err
	}
	h.client().Logger.Warn("discoverd service missing; recreating", "service", h.service)
	if h.closing.Load() {
		return nil
	}
	if addErr := h.client().AddService(h.service, h.serviceConfig); addErr != nil && !hh.IsObjectExistsError(addErr) {
		return err
	}
	if h.closing.Load() {
		return nil
	}
	h.Lock()
	inst = h.inst
	h.Unlock()
	if err = h.client().Put(path, inst, nil); err != nil {
		return err
	}
	if h.claimLeader {
		if leadErr := h.client().Service(h.service).SetLeader(inst.ID); leadErr != nil {
			h.client().Logger.Error("error restoring discoverd leader", "service", h.service, "err", leadErr)
		}
	}
	return nil
}

func expandAddr(addr string) string {
	if addr[0] == ':' {
		return os.Getenv("EXTERNAL_IP") + addr
	}
	return addr
}

func nextHeartbeat() time.Duration {
	if heartbeatJitter <= 0 {
		return heartbeatInterval
	}
	return heartbeatInterval - time.Duration(random.Math.Int63n(int64(heartbeatJitter)))
}

func nextHeartbeatFailing() time.Duration {
	return heartbeatFailingInterval + time.Duration(random.Math.Int63n(int64(heartbeatFailingInterval)))
}

// heartbeatMux flushes every registration on this client in one request.
// flynn-host holds one client for every user job on the host, so leader HTTP
// grows with clients, not with web process count.
type heartbeatMux struct {
	mu       sync.Mutex
	items    map[*heartbeater]struct{}
	stop     chan struct{}
	done     chan struct{}
	running  bool
	fallback bool
}

func (c *Client) trackHeartbeat(h *heartbeater) {
	c.hb.mu.Lock()
	defer c.hb.mu.Unlock()
	if c.hb.items == nil {
		c.hb.items = make(map[*heartbeater]struct{})
	}
	c.hb.items[h] = struct{}{}
	if c.hb.running {
		return
	}
	c.hb.running = true
	stop := make(chan struct{})
	done := make(chan struct{})
	c.hb.stop = stop
	c.hb.done = done
	go func() {
		defer close(done)
		c.heartbeatLoop(stop)
	}()
}

func (c *Client) untrackHeartbeat(h *heartbeater) {
	c.hb.mu.Lock()
	if c.hb.items != nil {
		delete(c.hb.items, h)
	}
	var done chan struct{}
	if c.hb.running && len(c.hb.items) == 0 {
		c.hb.running = false
		close(c.hb.stop)
		done = c.hb.done
		c.hb.stop = nil
		c.hb.done = nil
	}
	c.hb.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (c *Client) heartbeatLoop(stop <-chan struct{}) {
	timer := time.NewTimer(heartbeatDelay())
	defer timer.Stop()
	for {
		select {
		case <-stop:
			return
		case <-timer.C:
			delay := heartbeatDelay()
			if c.flushHeartbeats() {
				delay = heartbeatRetryDelay()
			}
			timer.Reset(delay)
		}
	}
}

// flushHeartbeats stamps every live registration on the leader. It reports
// whether the caller should retry on the short failure interval.
func (c *Client) flushHeartbeats() bool {
	c.hb.mu.Lock()
	beats := make([]*heartbeater, 0, len(c.hb.items))
	for h := range c.hb.items {
		beats = append(beats, h)
	}
	c.hb.mu.Unlock()

	stamps := make([]HeartbeatStamp, 0, len(beats))
	owners := make([]*heartbeater, 0, len(beats))
	for _, h := range beats {
		if h.closing.Load() {
			continue
		}
		h.Lock()
		stamp := HeartbeatStamp{Service: h.service, ID: h.inst.ID}
		h.Unlock()
		if stamp.Service == "" || stamp.ID == "" {
			continue
		}
		stamps = append(stamps, stamp)
		owners = append(owners, h)
	}
	if len(stamps) == 0 {
		return false
	}

	limit := heartbeatBatchLimit
	if limit < 1 {
		limit = MaxHeartbeatBatch
	}
	missing := make(map[string]struct{})
	failing := false
	for start := 0; start < len(stamps); start += limit {
		end := start + limit
		if end > len(stamps) {
			end = len(stamps)
		}
		var result HeartbeatBatchResult
		err := c.Post("/heartbeats", &HeartbeatBatch{Instances: stamps[start:end]}, &result)
		if errors.Is(err, errRouteMissing) {
			c.noteFallback(true)
			return c.flushEach(owners)
		}
		if err != nil {
			failing = true
			if c.Logger != nil {
				c.Logger.Error("heartbeat batch failed", "err", err)
			}
			continue
		}
		c.noteFallback(false)
		for _, m := range result.Missing {
			missing[m.Service+"\x00"+m.ID] = struct{}{}
		}
	}
	for _, h := range owners {
		if h.closing.Load() {
			continue
		}
		h.Lock()
		key := h.service + "\x00" + h.inst.ID
		h.Unlock()
		if _, ok := missing[key]; !ok {
			continue
		}
		if err := h.registerInstance(); err != nil {
			failing = true
			if c.Logger != nil {
				c.Logger.Error("heartbeat register failed", "service", h.service, "err", err)
			}
		}
	}
	return failing
}

func (c *Client) flushEach(owners []*heartbeater) bool {
	failing := false
	for _, h := range owners {
		if err := h.registerInstance(); err != nil {
			failing = true
			if c.Logger != nil {
				c.Logger.Error("heartbeat failed", "service", h.service, "addr", h.Addr(), "err", err)
			}
		}
	}
	return failing
}

func (c *Client) noteFallback(on bool) {
	c.hb.mu.Lock()
	defer c.hb.mu.Unlock()
	if c.hb.fallback == on {
		return
	}
	c.hb.fallback = on
	if c.Logger == nil {
		return
	}
	if on {
		c.Logger.Warn("discoverd has no heartbeat batch route; using per-instance PUT until the leader is upgraded")
		return
	}
	c.Logger.Info("discoverd heartbeat batch route accepted")
}
