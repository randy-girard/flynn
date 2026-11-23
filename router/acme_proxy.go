package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

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

const (
	// defaultACMETimeout bounds one responder attempt (dial, headers and
	// body) and how long a challenge request waits for the watches to start.
	defaultACMETimeout = 5 * time.Second
	// defaultACMERetryDelay is how long a failed watch start is remembered.
	// Challenge requests are unauthenticated and can name any host, so
	// without it each one would open a discoverd watch while discoverd
	// refuses or stalls.
	defaultACMERetryDelay = 5 * time.Second
	// acmeMaxBody caps a responder answer; a key authorization is ~90 bytes.
	acmeMaxBody = 64 << 10
)

// acmeStart is the one in-flight start of the acme watches. Requests that
// arrive while it runs wait on done instead of starting their own.
type acmeStart struct {
	done chan struct{}
}

func (s *HTTPListener) acmeTimeoutValue() time.Duration {
	if s.acmeTimeout > 0 {
		return s.acmeTimeout
	}
	return defaultACMETimeout
}

func (s *HTTPListener) acmeRetryDelayValue() time.Duration {
	if s.acmeRetryDelay > 0 {
		return s.acmeRetryDelay
	}
	return defaultACMERetryDelay
}

// acmeCaches returns the acme-challenge cache and the deny caches, creating
// them on first use. Only one start runs at a time; a failed start is
// remembered for acmeRetryDelay, and a caller waits at most acmeTimeout.
func (s *HTTPListener) acmeCaches(ctx context.Context) (*cache.ServiceCache, []*cache.ServiceCache, error) {
	s.mtx.RLock()
	acme, deny := s.acmeService, s.acmeDeny
	s.mtx.RUnlock()
	if acme != nil && deny != nil {
		return acme, deny, nil
	}

	s.mtx.Lock()
	switch {
	case s.closed:
		s.mtx.Unlock()
		return nil, nil, net.ErrClosed
	case s.acmeService != nil && s.acmeDeny != nil:
		acme, deny = s.acmeService, s.acmeDeny
		s.mtx.Unlock()
		return acme, deny, nil
	case s.acmeStarting == nil && time.Now().Before(s.acmeFailedUntil):
		err := s.acmeFailErr
		s.mtx.Unlock()
		return nil, nil, err
	}
	start := s.acmeStarting
	if start == nil {
		start = &acmeStart{done: make(chan struct{})}
		s.acmeStarting = start
		go s.startACMECaches(start)
	}
	s.mtx.Unlock()

	timer := time.NewTimer(s.acmeTimeoutValue())
	defer timer.Stop()
	select {
	case <-start.done:
	case <-timer.C:
		return nil, nil, errors.New("router: acme-challenge watch is still starting")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}

	s.mtx.RLock()
	defer s.mtx.RUnlock()
	if s.acmeService == nil || s.acmeDeny == nil {
		return nil, nil, s.acmeFailErr
	}
	return s.acmeService, s.acmeDeny, nil
}

// startACMECaches opens the watches for one acmeStart and records the result.
func (s *HTTPListener) startACMECaches(start *acmeStart) {
	acme, deny, err := s.newACMECaches()

	s.mtx.Lock()
	defer s.mtx.Unlock()
	defer close(start.done)
	s.acmeStarting = nil
	if err == nil && s.closed {
		err = net.ErrClosed
	}
	if err != nil {
		for _, sc := range append(deny, acme) {
			if sc != nil {
				sc.Close()
			}
		}
		s.acmeFailErr = err
		s.acmeFailedUntil = time.Now().Add(s.acmeRetryDelayValue())
		return
	}
	s.acmeService, s.acmeDeny = acme, deny
	s.acmeFailErr = nil
}

func (s *HTTPListener) newACMECaches() (acme *cache.ServiceCache, deny []*cache.ServiceCache, err error) {
	defer func() {
		if err != nil {
			for _, sc := range append(deny, acme) {
				if sc != nil {
					sc.Close()
				}
			}
			acme, deny = nil, nil
		}
	}()
	acme, err = cache.New(s.discoverd.Service(acmeChallengeService))
	if err != nil {
		acme = nil
		return
	}
	for _, name := range acmeDenyServices {
		// The service may not exist yet (no tenant job has started);
		// creating it empty lets the watch start.
		if err = s.discoverd.AddService(name, nil); err != nil && !hh.IsObjectExistsError(err) {
			return
		}
		var sc *cache.ServiceCache
		if sc, err = cache.New(s.discoverd.Service(name)); err != nil {
			return
		}
		deny = append(deny, sc)
	}
	return acme, deny, nil
}

// acmeInstances returns the acme-challenge instances whose IP is not a
// tenant or build overlay IP.
func acmeInstances(instances []*discoverd.Instance, deny []*cache.ServiceCache) []*discoverd.Instance {
	denied := map[string]struct{}{}
	for _, sc := range deny {
		for _, inst := range sc.Instances() {
			if ip, _, err := net.SplitHostPort(inst.Addr); err == nil {
				denied[ip] = struct{}{}
			}
		}
	}
	var allowed []*discoverd.Instance
	for _, inst := range instances {
		ip, _, err := net.SplitHostPort(inst.Addr)
		if err != nil {
			continue
		}
		if _, ok := denied[ip]; ok {
			continue
		}
		allowed = append(allowed, inst)
	}
	return allowed
}

// newACMEClient is the client every challenge request of a listener shares.
func newACMEClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			ResponseHeaderTimeout: timeout,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       90 * time.Second,
			DisableCompression:    true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type acmeAnswer struct {
	status      int
	contentType string
	body        []byte
}

// fetchACMEChallenge asks one responder for the challenge path.
func (s *HTTPListener) fetchACMEChallenge(ctx context.Context, inst *discoverd.Instance, req *http.Request) (*acmeAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, s.acmeTimeoutValue())
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+inst.Addr+req.URL.EscapedPath(), nil)
	if err != nil {
		return nil, err
	}
	out.Host = req.Host
	res, err := s.acmeClient.Do(out)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, acmeMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > acmeMaxBody {
		return nil, errors.New("router: acme-challenge answer too large")
	}
	return &acmeAnswer{status: res.StatusCode, contentType: res.Header.Get("Content-Type"), body: body}, nil
}

// serveACMEChallenge asks each allowed acme-challenge instance in turn and
// returns the first 200. A token lives in one responder's memory, so with
// two responders (a deploy's old and new worker) the other one says 404.
// With no 200 the first responder answer is returned, or 502 when none
// answered.
func (s *HTTPListener) serveACMEChallenge(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		fail(w, http.StatusMethodNotAllowed)
		return
	}
	ctx := req.Context()
	acme, deny, err := s.acmeCaches(ctx)
	if err != nil {
		fail(w, 503)
		return
	}
	instances := acmeInstances(acme.Instances(), deny)
	if len(instances) == 0 {
		fail(w, 503)
		return
	}
	var first *acmeAnswer
	for _, inst := range instances {
		ans, err := s.fetchACMEChallenge(ctx, inst, req)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if ans.status == http.StatusOK {
			writeACMEAnswer(w, req, ans)
			return
		}
		if first == nil {
			first = ans
		}
	}
	if first == nil {
		fail(w, 502)
		return
	}
	writeACMEAnswer(w, req, first)
}

func writeACMEAnswer(w http.ResponseWriter, req *http.Request, ans *acmeAnswer) {
	if ans.contentType != "" {
		w.Header().Set("Content-Type", ans.contentType)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(ans.body)))
	w.WriteHeader(ans.status)
	if req.Method != http.MethodHead {
		w.Write(ans.body)
	}
}
