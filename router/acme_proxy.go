package main

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/randy-girard/flynn/discoverd/cache"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

const acmeChallengeService = "acme-challenge"

// acmeDenyServices are the overlay services flynn-host publishes for tenant
// and build job IPs. An acme-challenge instance on one of those IPs is a
// tenant job, never the system responder (EDGE-01).
var acmeDenyServices = []string{netpolicy.ServiceUser, netpolicy.ServiceBuild}

// acmeCaches returns the acme-challenge cache and the deny caches, creating
// them on first use. A failed start is retried on the next request.
func (s *HTTPListener) acmeCaches() (*cache.ServiceCache, []*cache.ServiceCache, error) {
	s.mtx.RLock()
	acme, deny := s.acmeService, s.acmeDeny
	s.mtx.RUnlock()
	if acme != nil && deny != nil {
		return acme, deny, nil
	}

	var created []*cache.ServiceCache
	closeCreated := func() {
		for _, sc := range created {
			sc.Close()
		}
	}
	if acme == nil {
		sc, err := cache.New(s.discoverd.Service(acmeChallengeService))
		if err != nil {
			return nil, nil, err
		}
		created = append(created, sc)
		acme = sc
	}
	if deny == nil {
		for _, name := range acmeDenyServices {
			// The service may not exist yet (no tenant job has started);
			// creating it empty lets the watch start.
			if err := s.discoverd.AddService(name, nil); err != nil && !hh.IsObjectExistsError(err) {
				closeCreated()
				return nil, nil, err
			}
			sc, err := cache.New(s.discoverd.Service(name))
			if err != nil {
				closeCreated()
				return nil, nil, err
			}
			created = append(created, sc)
			deny = append(deny, sc)
		}
	}

	s.mtx.Lock()
	defer s.mtx.Unlock()
	if s.closed {
		closeCreated()
		return nil, nil, net.ErrClosed
	}
	keep := map[*cache.ServiceCache]bool{}
	if s.acmeService == nil {
		s.acmeService = acme
		keep[acme] = true
	}
	if s.acmeDeny == nil {
		s.acmeDeny = deny
		for _, sc := range deny {
			keep[sc] = true
		}
	}
	for _, sc := range created {
		if !keep[sc] {
			sc.Close()
		}
	}
	return s.acmeService, s.acmeDeny, nil
}

// acmeInstance picks the first acme-challenge instance whose IP is not a
// tenant or build overlay IP.
func acmeInstance(instances []*discoverd.Instance, deny []*cache.ServiceCache) *discoverd.Instance {
	denied := map[string]struct{}{}
	for _, sc := range deny {
		for _, inst := range sc.Instances() {
			if ip, _, err := net.SplitHostPort(inst.Addr); err == nil {
				denied[ip] = struct{}{}
			}
		}
	}
	for _, inst := range instances {
		ip, _, err := net.SplitHostPort(inst.Addr)
		if err != nil {
			continue
		}
		if _, ok := denied[ip]; ok {
			continue
		}
		return inst
	}
	return nil
}

func (s *HTTPListener) serveACMEChallenge(w http.ResponseWriter, req *http.Request) {
	acme, deny, err := s.acmeCaches()
	if err != nil {
		fail(w, 503)
		return
	}
	instance := acmeInstance(acme.Instances(), deny)
	if instance == nil {
		fail(w, 503)
		return
	}
	target, err := url.Parse("http://" + instance.Addr)
	if err != nil {
		fail(w, 500)
		return
	}
	httputil.NewSingleHostReverseProxy(target).ServeHTTP(w, req)
}
