package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/julienschmidt/httprouter"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

func getOverlayIP(t *testing.T, api *jobAPI, ip string) (int, *host.OverlayIP) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/host/netpolicy/ips/"+ip, nil)
	api.GetOverlayIP(rr, req, httprouter.Params{{Key: "ip", Value: ip}})
	if rr.Code != http.StatusOK {
		return rr.Code, nil
	}
	var res host.OverlayIP
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return rr.Code, &res
}

// CORE-25-F1: discoverd DNS asks the local host about an overlay IP that it
// has not seen registered yet. The answer is the registration the host is
// publishing for the job that owns the IP right now, including a user job's
// datastore allowlist, and 404 for an IP no live job owns.
func TestGetOverlayIPReturnsLiveJobRegistration(t *testing.T) {
	state := NewState("host-test", filepath.Join(t.TempDir(), "host-state-db"))
	if err := state.OpenDB(); err != nil {
		t.Fatal(err)
	}
	defer state.CloseDB()
	h := &Host{id: "host-test", state: state}
	api := &jobAPI{host: h}

	add := func(job *host.Job, ip string) {
		t.Helper()
		if err := state.AddJob(job); err != nil {
			t.Fatal(err)
		}
		if ip != "" {
			state.SetContainerIP(job.ID, parseIP(t, ip))
		}
	}
	user := &host.Job{ID: "job-user", Metadata: map[string]string{
		"flynn-controller.app":          "app-1",
		netpolicy.MetaJobDiscoverdAllow: "redis-lagoon-59415",
	}}
	add(user, "100.100.1.5")
	add(&host.Job{ID: "job-sys", Partition: "system", Metadata: map[string]string{
		"flynn-controller.app_name": "controller",
	}}, "100.100.1.6")
	// The last job on 100.100.1.7 has exited; a stopped job owns nothing.
	add(&host.Job{ID: "job-done", Metadata: map[string]string{}}, "100.100.1.7")
	state.SetStatusDone("job-done", 0)

	code, res := getOverlayIP(t, api, "100.100.1.5")
	if code != 200 || res.JobID != "job-user" || res.IP != "100.100.1.5" {
		t.Fatalf("user ip: %d %+v", code, res)
	}
	if res.Meta["class"] != "user" || res.Meta[netpolicy.MetaDiscoverdAllow] != "redis-lagoon-59415" {
		t.Fatalf("user meta = %v", res.Meta)
	}

	code, res = getOverlayIP(t, api, "100.100.1.6")
	if code != 200 || res.JobID != "job-sys" || res.Meta["class"] != "system" {
		t.Fatalf("system ip: %d %+v", code, res)
	}

	for _, ip := range []string{"100.100.1.7", "100.100.1.9"} {
		if code, _ := getOverlayIP(t, api, ip); code != 404 {
			t.Fatalf("%s: status %d, want 404", ip, code)
		}
	}
	if code, _ := getOverlayIP(t, api, "not-an-ip"); code != 400 {
		t.Fatalf("bad ip: status %d, want 400", code)
	}
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad ip %q", s)
	}
	return ip
}
