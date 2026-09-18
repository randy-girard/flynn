package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/randy-girard/flynn/discoverd/cache"
	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
)

func main() {
	handler := newStatusHandler(status.Handler(GetStatus), os.Getenv("AUTH_KEY"))
	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), httphelper.CORSAllowAll.Handler(handler)))
}

type statusHandler struct {
	h         status.Handler
	k         string
	whitelist []*net.IPNet
}

func newStatusHandler(h status.Handler, k string) *statusHandler {
	whitelist := []*net.IPNet{
		mustParseCIDR("10.0.0.0/8"),      // 10.0.0.0 - 10.255.255.255
		mustParseCIDR("172.16.0.0/12"),   // 172.16.0.0 - 172.31.255.255
		mustParseCIDR("192.168.0.0/16"),  // 192.168.0.0 - 192.168.255.255
		mustParseCIDR("127.0.0.0/8"),     // 127.0.0.0 - 127.255.255.255
		mustParseCIDR("fd00::/8"),        // fdxx:xxxx:xxxx...
		mustParseCIDR("::1/128"),         // ::1
		mustParseCIDR("192.0.2.0/24"),    // TEST-NET (for containers)
		mustParseCIDR("198.51.100.0/24"), // TEST-NET (for containers)
		mustParseCIDR("203.0.113.0/24"),  // TEST-NET (for containers)
		mustParseCIDR("100.64.0.0/10"),   // for containers
	}
	return &statusHandler{h, k, whitelist}
}

func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(fmt.Sprintf("ParseCIDR(%q): %v", s, err))
	}
	return n
}

func (s *statusHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.k != "" {
		var addr string
		if fwdFor := r.Header.Get("X-Forwarded-For"); fwdFor != "" {
			if v := strings.Split(fwdFor, ", "); len(v) > 0 {
				addr = strings.TrimSpace(v[len(v)-1])
			}
		}
		if addr == "" {
			if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
				addr = clientIP
			}
		}

		var authed bool
		if ip := net.ParseIP(addr); ip != nil {
			for _, v := range s.whitelist {
				if v.Contains(ip) {
					authed = true
					break
				}
			}
		}

		if !authed {
			_, password, _ := r.BasicAuth()
			if password == "" {
				password = r.URL.Query().Get("key")
			}
			authed = len(password) == len(s.k) && subtle.ConstantTimeCompare([]byte(password), []byte(s.k)) == 1
		}

		if !authed {
			w.WriteHeader(401)
			return
		}
	}

	s.h.ServeHTTP(w, r)
}

var httpClient = &http.Client{Timeout: 2 * time.Second}

type ReqFn func() (*http.Request, error)

type Service struct {
	Name     string
	ReqFn    func() (*http.Request, error)
	Optional bool
}

func (s Service) Status() status.Status {
	req, err := s.ReqFn()
	if err != nil {
		return status.Unhealthy
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return status.Unhealthy
	}
	defer res.Body.Close()

	var data struct {
		Data status.Status
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		return status.Unhealthy
	}
	return data.Data
}

var services = []Service{
	{Name: "blobstore"},
	{Name: "controller", ReqFn: controllerReqFn},
	{Name: "controller-scheduler", ReqFn: lazyReqFn(func() ReqFn { return LeaderReqFn("controller-scheduler", "") })},
	{Name: "controller-worker"},
	{Name: "discoverd"},
	{Name: "flannel"},
	{Name: "gitreceive", ReqFn: lazyReqFn(func() ReqFn { return RandomReqFn("gitreceive") })},
	{Name: "tarreceive", ReqFn: lazyReqFn(func() ReqFn { return RandomReqFn("tarreceive") }), Optional: true},
	{Name: "logaggregator", ReqFn: lazyReqFn(func() ReqFn { return LeaderReqFn("logaggregator", "80") })},
	{Name: "postgres", ReqFn: lazyReqFn(func() ReqFn { return LeaderReqFn("postgres", "5433") })},
	{Name: "router", ReqFn: lazyReqFn(func() ReqFn { return RandomReqFn("router-api") })},
}

func controllerReqFn() (*http.Request, error) {
	instances, err := discoverd.GetInstances("controller", 1*time.Second)
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, errors.New("no controller instances")
	}
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s%s", instances[0].Addr, status.Path), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth("", instances[0].Meta["AUTH_KEY"])
	return req, nil
}

func lazyReqFn(makeFn func() ReqFn) ReqFn {
	var (
		once sync.Once
		fn   ReqFn
	)
	return func() (*http.Request, error) {
		once.Do(func() { fn = makeFn() })
		return fn()
	}
}

func RandomReqFn(name string) ReqFn {
	sc, err := cache.New(discoverd.NewService(name))
	if err != nil {
		log.Fatalf("error creating %s cache: %s", name, err)
	}
	return func() (*http.Request, error) {
		addrs := sc.Addrs()
		if len(addrs) == 0 {
			return nil, errors.New("no service instances")
		}
		return http.NewRequest("GET", fmt.Sprintf("http://%s%s", addrs[rand.Intn(len(addrs))], status.Path), nil)
	}
}

func LeaderReqFn(name, port string) ReqFn {
	events := make(chan *discoverd.Event)
	if _, err := discoverd.NewService(name).Watch(events); err != nil {
		log.Fatalf("error creating %s cache: %s", name, err)
	}
	var leader atomic.Value // addr string
	leader.Store("")
	go func() {
		for e := range events {
			if e.Kind != discoverd.EventKindLeader || e.Instance == nil {
				continue
			}
			leader.Store(e.Instance.Addr)
		}
	}()
	return func() (*http.Request, error) {
		addr := leader.Load().(string)
		if addr == "" {
			return nil, errors.New("no leader")
		}
		if port != "" {
			host, _, _ := net.SplitHostPort(addr)
			addr = net.JoinHostPort(host, port)
		}
		return http.NewRequest("GET", fmt.Sprintf("http://%s%s", addr, status.Path), nil)
	}
}

var serviceReqOnce sync.Once

func ensureServiceReqFns() {
	serviceReqOnce.Do(func() {
		for i, s := range services {
			if s.ReqFn != nil {
				continue
			}
			name := s.Name
			services[i].ReqFn = lazyReqFn(func() ReqFn { return RandomReqFn(name) })
		}
	})
}

type ServiceStatus struct {
	Name     string
	Status   status.Status
	optional bool
}

func GetStatus() status.Status {
	ensureServiceReqFns()
	results := make(chan ServiceStatus)
	for _, s := range services {
		go func(s Service) {
			results <- ServiceStatus{s.Name, s.Status(), s.Optional}
		}(s)
	}

	data := make(map[string]status.Status, len(services))
	healthy := true
	for i := 0; i < len(services); i++ {
		res := <-results
		data[res.Name] = res.Status
		if res.Status.Status != status.CodeHealthy && !res.optional {
			healthy = false
		}
	}

	s, _ := status.New(healthy, data)
	return s
}
