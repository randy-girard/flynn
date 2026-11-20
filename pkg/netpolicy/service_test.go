package netpolicy

import (
	"strings"
	"testing"

	host "github.com/randy-girard/flynn/host/types"
)

func TestUserServiceAllowed(t *testing.T) {
	for _, tc := range []struct {
		app, service string
		want         bool
	}{
		{"shop", "shop", true},
		{"shop", "shop-web", true},
		{"shop", "shop-admin-web", true},
		{"shop", "controller", false},
		{"shop", "blobstore", false},
		{"shop", "acme-challenge", false},
		{"shop", "postgres", false},
		{"shop", "other-web", false},
		{"shop", "shopper-web", false},
		{"shop", "", false},
		{"", "shop-web", false},
		// tenant app names that prefix a system service
		{"controller-scheduler", "controller-scheduler", false},
		{"router", "router-api", false},
		{"status", "status-web", false},
		{"acme", "acme-challenge", false},
		{"partial", "partial-logger", false},
		{"flynn", "flynn-host", false},
		{"flynn-net", "flynn-net-user", false},
		{"flynn-docs", "flynn-docs-web", true},
		// isolated datastore and UUID appliance names
		{"redis-lagoon", "redis-lagoon-59415", false},
		{"pg-ridge", "pg-ridge-ffpade", false},
		{"redis", "redis-8e2a5a1c-4a0b-4c8e-9d3e-1b2c3d4e5f60", false},
		{"redis-cache", "redis-cache-web", true},
	} {
		if got := UserServiceAllowed(tc.app, tc.service); got != tc.want {
			t.Errorf("UserServiceAllowed(%q, %q) = %v, want %v", tc.app, tc.service, got, tc.want)
		}
	}
}

func TestUserJobServiceError(t *testing.T) {
	job := func(name string, meta map[string]string) *host.Job {
		j := &host.Job{Metadata: meta}
		j.Config.Ports = []host.Port{
			{Port: 8080, Proto: "tcp", Service: &host.Service{Name: "shop-web", Create: true}},
			{Port: 9090, Proto: "tcp"},
		}
		if name != "" {
			j.Config.Ports = append(j.Config.Ports, host.Port{Port: 9091, Proto: "tcp", Service: &host.Service{Name: name}})
		}
		return j
	}
	user := map[string]string{"flynn-controller.app_name": "shop", "flynn-controller.type": "web"}
	if err := UserJobServiceError(job("", user)); err != nil {
		t.Fatalf("own services: %v", err)
	}
	for _, name := range []string{"controller", "blobstore", "acme-challenge", "other-web"} {
		err := UserJobServiceError(job(name, user))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("%s: err=%v", name, err)
		}
	}
	if err := UserJobServiceError(job("", map[string]string{"flynn-controller.type": "web"})); err == nil {
		t.Fatal("a user job without an app name may not register services")
	}
	if err := UserJobServiceError(&host.Job{Metadata: map[string]string{"flynn-controller.type": "web"}}); err != nil {
		t.Fatalf("no services: %v", err)
	}
	sys := job("controller", map[string]string{"flynn-system-app": "true", "flynn-controller.app_name": "controller"})
	sys.Partition = "system"
	if err := UserJobServiceError(sys); err != nil {
		t.Fatalf("system jobs keep their names: %v", err)
	}
}
