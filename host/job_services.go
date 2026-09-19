package main

import (
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/inconshreveable/log15"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/discoverd/health"
	host "github.com/randy-girard/flynn/host/types"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

func hostRegistersServices(job *host.Job) bool {
	return netpolicy.ClassifyJob(job) == netpolicy.ClassUser
}

func jobServiceInstance(env map[string]string, ip string, port host.Port) *discoverd.Instance {
	inst := &discoverd.Instance{
		Addr:  fmt.Sprintf("%s:%v", ip, port.Port),
		Proto: port.Proto,
	}
	for k, v := range env {
		if _, ok := discoverd.EnvInstanceMeta[k]; !ok {
			continue
		}
		if inst.Meta == nil {
			inst.Meta = make(map[string]string)
		}
		inst.Meta[k] = v
	}
	return inst
}

func (c *Container) closeJobServices() {
	c.svcMu.Lock()
	hbs := c.svcHBs
	c.svcHBs = nil
	c.svcMu.Unlock()
	for _, hb := range hbs {
		if hb != nil {
			_ = hb.Close()
		}
	}
}

func (c *Container) registerJobServices(log log15.Logger) error {
	if !hostRegistersServices(c.job) {
		return nil
	}
	client := c.l.discoverdClient
	if client == nil {
		return fmt.Errorf("discoverd client is not configured")
	}
	ip := ""
	if c.IP != nil {
		ip = c.IP.String()
	} else if v := c.job.Config.Env["EXTERNAL_IP"]; v != "" {
		ip = v
	}
	if ip == "" {
		return nil
	}
	var hbs []discoverd.Heartbeater
	for _, port := range c.job.Config.Ports {
		if port.Service == nil {
			continue
		}
		plog := log.New("name", port.Service.Name, "port", port.Port, "proto", port.Proto)
		hb, err := registerJobPort(client, c.job.Config.Env, ip, port, func() {
			_ = c.Signal(int(syscall.SIGKILL))
		}, plog)
		if err != nil {
			for _, h := range hbs {
				_ = h.Close()
			}
			return err
		}
		hbs = append(hbs, hb)
	}
	c.svcMu.Lock()
	c.svcHBs = hbs
	c.svcMu.Unlock()
	return nil
}

func registerJobPort(client *discoverd.Client, env map[string]string, ip string, port host.Port, kill func(), log log15.Logger) (discoverd.Heartbeater, error) {
	config := port.Service
	if config.Create {
		if err := client.AddService(config.Name, nil); err != nil && !hh.IsObjectExistsError(err) {
			return nil, fmt.Errorf("discoverd: %s", err)
		}
	}
	inst := jobServiceInstance(env, ip, port)
	if config.Check == nil {
		log.Info("registering instance from flynn-host", "addr", inst.Addr)
		return client.RegisterInstance(config.Name, inst)
	}

	var check health.Check
	switch config.Check.Type {
	case "tcp":
		check = &health.TCPCheck{Addr: inst.Addr}
	case "http", "https":
		check = &health.HTTPCheck{
			URL:        fmt.Sprintf("%s://%s%s", config.Check.Type, inst.Addr, config.Check.Path),
			Host:       config.Check.Host,
			StatusCode: config.Check.Status,
			MatchBytes: []byte(config.Check.Match),
		}
	default:
		return nil, fmt.Errorf("unsupported check type: %s", config.Check.Type)
	}
	log.Info("adding healthcheck from flynn-host", "type", config.Check.Type)
	reg := health.Registration{
		Registrar: client,
		Service:   config.Name,
		Instance:  inst,
		Monitor: health.Monitor{
			Interval:  config.Check.Interval,
			Threshold: config.Check.Threshold,
			Logger:    log.New("component", "monitor"),
		}.Run,
		Check:  check,
		Logger: log,
	}
	if config.Check.KillDown {
		reg.Events = make(chan health.MonitorEvent)
		go func() {
			if config.Check.StartTimeout == 0 {
				config.Check.StartTimeout = 10 * time.Second
			}
			start := false
			lastStatus := health.MonitorStatusDown
			var mtx sync.Mutex
			maybeKill := func() {
				if lastStatus == health.MonitorStatusDown && kill != nil {
					log.Warn("killing the job")
					kill()
				}
			}
			go func() {
				<-time.After(config.Check.StartTimeout)
				mtx.Lock()
				defer mtx.Unlock()
				maybeKill()
				start = true
			}()
			for e := range reg.Events {
				mtx.Lock()
				lastStatus = e.Status
				if start {
					maybeKill()
				}
				mtx.Unlock()
			}
		}()
	}
	return reg.Register(), nil
}
