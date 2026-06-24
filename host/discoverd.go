package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/host/logmux"
	"github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/inconshreveable/log15"
)

var discoverdLogger = log15.New("component", "discoverd-manager")

func NewDiscoverdManager(backend Backend, sinkManager *logmux.SinkManager, hostID, publishAddr string, tags map[string]string) *DiscoverdManager {
	d := &DiscoverdManager{
		backend:     backend,
		sinkManager: sinkManager,
		inst: &discoverd.Instance{
			Addr: publishAddr,
			Meta: map[string]string{"id": hostID},
		},
	}
	for k, v := range tags {
		d.inst.Meta[host.TagPrefix+k] = v
	}
	d.local.Store(false)
	return d
}

type DiscoverdManager struct {
	backend     Backend
	sinkManager *logmux.SinkManager
	inst        *discoverd.Instance
	mtx         sync.Mutex
	hb          discoverd.Heartbeater
	local       atomic.Value // bool
}

func (d *DiscoverdManager) Close() error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.hb != nil {
		// explicitly indicate in the metadata that the host is
		// shutting down so that the scheduler removes the host
		// immediately (rather than treating it as unhealthy for a
		// short time)
		d.inst.Meta["shutdown"] = "true"
		d.hb.SetMeta(d.inst.Meta)

		err := d.hb.Close()
		d.hb = nil
		return err
	}
	return nil
}

func (d *DiscoverdManager) localConnected() bool {
	return d.local.Load().(bool)
}

func (d *DiscoverdManager) heartbeat(url string) error {
	disc := discoverd.NewClientWithURL(url)

	// Fast path: if a heartbeater already exists, just retarget it at the
	// new URL.
	d.mtx.Lock()
	if d.hb != nil {
		d.hb.SetClient(disc)
		d.mtx.Unlock()
		return nil
	}
	// Snapshot the instance under the lock so the slow registration call
	// below can run without the mutex held, which lets ConnectPeer attempt
	// multiple peers concurrently.
	snap := &discoverd.Instance{
		Addr:  d.inst.Addr,
		Proto: d.inst.Proto,
		Meta:  make(map[string]string, len(d.inst.Meta)),
	}
	for k, v := range d.inst.Meta {
		snap.Meta[k] = v
	}
	d.mtx.Unlock()

	hb, err := disc.AddServiceAndRegisterInstance("flynn-host", snap)
	if err != nil {
		return err
	}

	// Commit. If another concurrent attempt won the race, drop ours and
	// retarget the winning heartbeater at this URL.
	d.mtx.Lock()
	defer d.mtx.Unlock()
	if d.hb != nil {
		hb.Close()
		d.hb.SetClient(disc)
		return nil
	}
	d.hb = hb
	return nil
}

func (d *DiscoverdManager) ConnectLocal(url string) error {
	if d.localConnected() {
		return errors.New("host: discoverd is already configured")
	}

	if err := d.heartbeat(url); err != nil {
		return err
	}
	d.local.Store(true)

	d.backend.SetDefaultEnv("DISCOVERD", url)
	os.Setenv("DISCOVERD", url)
	discoverd.DefaultClient = discoverd.NewClient()

	go func() {
		if err := d.sinkManager.StreamToAggregators(discoverd.NewClientWithURL(url).Service("logaggregator")); err != nil {
			shutdown.Fatal(err)
		}
	}()

	return nil
}

func (d *DiscoverdManager) ConnectPeer(ips []string) error {
	if d.localConnected() {
		return nil
	}
	if len(ips) == 0 {
		return errors.New("host: no discoverd peers available")
	}

	// Attempt each peer concurrently and return as soon as one succeeds.
	// The discoverd client retries connection-refused errors internally for
	// up to ~60s, so trying peers serially makes startup block on a single
	// unresponsive peer (typically the local one immediately after a
	// daemon restart, before the local discoverd job has been resurrected).
	type result struct {
		ip  string
		err error
	}
	results := make(chan result, len(ips))
	for _, ip := range ips {
		go func(ip string) {
			discoverdLogger.Debug("attempting to connect to discoverd peer", "ip", ip)
			url := fmt.Sprintf("http://%s:1111", ip)
			if err := d.heartbeat(url); err != nil {
				discoverdLogger.Debug("failed to connect to discoverd peer", "ip", ip, "err", err)
				results <- result{ip: ip, err: err}
				return
			}
			discoverdLogger.Info("connected to discoverd peer", "ip", ip)
			results <- result{ip: ip, err: nil}
		}(ip)
	}

	var lastErr error
	for i := 0; i < len(ips); i++ {
		r := <-results
		if r.err == nil {
			return nil
		}
		lastErr = r.err
	}
	return lastErr
}

func (d *DiscoverdManager) UpdateTags(tags map[string]string) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	for k, v := range tags {
		name := host.TagPrefix + k
		// treat empty tags as ones to delete
		if v == "" {
			delete(d.inst.Meta, name)
			continue
		}
		d.inst.Meta[name] = v
	}
	if d.hb == nil {
		return nil
	}
	return d.hb.SetMeta(d.inst.Meta)
}
