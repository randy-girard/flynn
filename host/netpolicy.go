package main

import (
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"

	discoverd "github.com/flynn/flynn/discoverd/client"
	host "github.com/flynn/flynn/host/types"
	"github.com/flynn/flynn/pkg/iptables"
	"github.com/flynn/flynn/pkg/netpolicy"
	"github.com/inconshreveable/log15"
)

// netPolicy tracks overlay IPs in discoverd (cluster-wide) and mirrors them
// into ipsets so iptables can isolate user jobs on every host.
type pendingIP struct {
	job *host.Job
	ip  net.IP
}

type netPolicy struct {
	log log15.Logger

	mu      sync.Mutex
	local   map[string]discoverd.Heartbeater // job ID → heartbeat
	pending map[string]pendingIP
	client  *discoverd.Client
}

func newNetPolicy(log log15.Logger) *netPolicy {
	return &netPolicy{
		log:     log.New("component", "netpolicy"),
		local:   make(map[string]discoverd.Heartbeater),
		pending: make(map[string]pendingIP),
	}
}

func (p *netPolicy) Start(client *discoverd.Client) {
	if client == nil {
		return
	}
	p.mu.Lock()
	if p.client != nil {
		p.mu.Unlock()
		return
	}
	p.client = client
	pending := make([]pendingIP, 0, len(p.pending))
	for _, e := range p.pending {
		pending = append(pending, e)
	}
	p.mu.Unlock()

	for _, e := range pending {
		p.register(e.job, e.ip)
	}
	for _, svc := range iptables.IsolationSets() {
		go p.watch(svc)
	}
}

func (p *netPolicy) Track(job *host.Job, ip net.IP) {
	if job == nil || ip == nil || job.Config.HostNetwork {
		return
	}
	class := netpolicy.ClassifyJob(job)
	set := netpolicy.ServiceForClass(class)
	if err := iptables.AddSetIP(set, ip); err != nil {
		p.log.Error("ipset add", "set", set, "ip", ip, "err", err)
	}
	p.mu.Lock()
	p.pending[job.ID] = pendingIP{job: job, ip: ip}
	p.mu.Unlock()
	p.register(job, ip)
}

func (p *netPolicy) register(job *host.Job, ip net.IP) {
	if job == nil || ip == nil {
		return
	}
	p.mu.Lock()
	client := p.client
	if _, ok := p.local[job.ID]; ok {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	if client == nil {
		return
	}
	class := netpolicy.ClassifyJob(job)
	svc := netpolicy.ServiceForClass(class)
	inst := &discoverd.Instance{
		Addr:  net.JoinHostPort(ip.String(), netpolicy.DummyPort),
		Proto: "tcp",
		Meta:  map[string]string{"class": class.String(), "job.id": job.ID},
	}
	hb, err := client.AddServiceAndRegisterInstance(svc, inst)
	if err != nil {
		p.log.Error("register overlay ip", "service", svc, "ip", ip, "err", err)
		return
	}
	p.mu.Lock()
	p.local[job.ID] = hb
	p.mu.Unlock()
}

func (p *netPolicy) Untrack(job *host.Job, ip net.IP) {
	if job == nil {
		return
	}
	class := netpolicy.ClassifyJob(job)
	if ip != nil {
		_ = iptables.DelSetIP(netpolicy.ServiceForClass(class), ip)
	}
	p.mu.Lock()
	hb := p.local[job.ID]
	delete(p.local, job.ID)
	delete(p.pending, job.ID)
	p.mu.Unlock()
	if hb != nil {
		_ = hb.Close()
	}
}

func (p *netPolicy) watch(service string) {
	for {
		p.mu.Lock()
		client := p.client
		p.mu.Unlock()
		if client == nil {
			return
		}
		events := make(chan *discoverd.Event)
		stream, err := client.Service(service).Watch(events)
		if err != nil {
			p.log.Error("watch", "service", service, "err", err)
			time.Sleep(time.Second)
			continue
		}
		// Watch starts with EventUp for each current instance, then
		// EventKindCurrent. Flushing the ipset on every Up races with
		// Track(): Instances() can lag the event, ReplaceSetIPs drops the
		// local redis/postgres IP, and nothing adds it back (3-node
		// upgrade-2 redis PING timeout). Apply Up/Down incrementally;
		// only Current does a full replace, unioned with local jobs.
		for ev := range events {
			if ev == nil {
				continue
			}
			switch ev.Kind {
			case discoverd.EventKindUp, discoverd.EventKindUpdate:
				if ip := overlayInstanceIP(ev.Instance); ip != nil {
					if err := iptables.AddSetIP(service, ip); err != nil {
						p.log.Error("ipset add", "set", service, "ip", ip, "err", err)
					}
				}
			case discoverd.EventKindDown:
				if ip := overlayInstanceIP(ev.Instance); ip != nil && !p.isLocalIP(ip) {
					_ = iptables.DelSetIP(service, ip)
				}
			case discoverd.EventKindCurrent:
				if err := p.syncSet(client, service); err != nil {
					p.log.Error("sync ipset", "service", service, "err", err)
				}
			}
		}
		stream.Close()
		time.Sleep(time.Second)
	}
}

func overlayInstanceIP(inst *discoverd.Instance) net.IP {
	if inst == nil {
		return nil
	}
	return netpolicy.InstanceHost(inst.Addr)
}

func (p *netPolicy) isLocalIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range p.pending {
		if e.ip != nil && e.ip.Equal(ip) {
			return true
		}
	}
	return false
}

func (p *netPolicy) localIPs(service string) []net.IP {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ips []net.IP
	for _, e := range p.pending {
		if e.job == nil || e.ip == nil {
			continue
		}
		if netpolicy.ServiceForClass(netpolicy.ClassifyJob(e.job)) == service {
			ips = append(ips, e.ip)
		}
	}
	return ips
}

func (p *netPolicy) syncSet(client *discoverd.Client, service string) error {
	insts, err := client.Service(service).Instances()
	if err != nil {
		if discoverd.IsNotFound(err) {
			return iptables.ReplaceSetIPs(service, p.localIPs(service))
		}
		return err
	}
	addrs := make([]net.IP, 0, len(insts))
	for _, inst := range insts {
		if ip := overlayInstanceIP(inst); ip != nil {
			addrs = append(addrs, ip)
		}
	}
	return iptables.ReplaceSetIPs(service, iptables.UnionIPs(addrs, p.localIPs(service)))
}

func enableBridgeNetfilter() error {
	path := "/proc/sys/net/bridge/bridge-nf-call-iptables"
	if _, err := os.Stat(path); err != nil {
		_ = exec.Command("modprobe", "br_netfilter").Run()
	}
	if err := ioutil.WriteFile(path, []byte("1\n"), 0644); err != nil {
		return fmt.Errorf("enable br_netfilter: %w", err)
	}
	return nil
}
