package discoverd

import (
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
}

func (h *heartbeater) Close() error {
	h.closeOnce.Do(func() {
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
	h.c.Store(c)
}

func (h *heartbeater) client() *Client {
	return h.c.Load().(*Client)
}

const (
	heartbeatInterval        = 5 * time.Second
	heartbeatJitter          = 2 * time.Second
	heartbeatFailingInterval = 200 * time.Millisecond
)

func (h *heartbeater) run(firstErr chan<- error) {
	register := func() error {
		return h.registerInstance()
	}

	err := register()
	firstErr <- err
	if err != nil {
		return
	}
	timer := time.NewTimer(nextHeartbeat())
	for {
		select {
		case <-timer.C:
			if err := register(); err != nil {
				h.client().Logger.Error("heartbeat failed", "service", h.service, "addr", h.inst.Addr, "err", err)
				timer.Reset(nextHeartbeatFailing())
				break
			}
			timer.Reset(nextHeartbeat())
		case <-h.stop:
			h.client().Delete(h.instancePath())
			close(h.done)
			return
		}
	}
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
	h.Lock()
	inst := h.inst
	h.Unlock()
	path := fmt.Sprintf("/services/%s/instances/%s", h.service, inst.ID)
	err := h.client().Put(path, inst, nil)
	if !missingService(err) {
		return err
	}
	h.client().Logger.Warn("discoverd service missing; recreating", "service", h.service)
	if addErr := h.client().AddService(h.service, h.serviceConfig); addErr != nil && !hh.IsObjectExistsError(addErr) {
		return err
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
	return heartbeatInterval - time.Duration(random.Math.Int63n(int64(heartbeatJitter)))
}

func nextHeartbeatFailing() time.Duration {
	return heartbeatFailingInterval + time.Duration(random.Math.Int63n(int64(heartbeatFailingInterval)))
}
