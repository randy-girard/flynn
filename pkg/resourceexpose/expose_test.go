package resourceexpose

import (
	"reflect"
	"testing"

	router "github.com/randy-girard/flynn/router/types"
)

func TestLookupAndResolve(t *testing.T) {
	s, err := Lookup("postgres")
	if err != nil || s.DefaultService != "postgres" || !s.Leader {
		t.Fatalf("postgres: %+v %v", s, err)
	}
	app, svc := s.ResolveAppService(map[string]string{"FLYNN_POSTGRES": "postgres"})
	if app != "postgres" || svc != "postgres" {
		t.Fatalf("resolve postgres %s %s", app, svc)
	}

	mysql, err := Lookup("mysql")
	if err != nil {
		t.Fatal(err)
	}
	app, svc = mysql.ResolveAppService(map[string]string{"FLYNN_MYSQL": "mariadb"})
	if app != "mariadb" || svc != "mariadb" {
		t.Fatalf("mysql %s %s", app, svc)
	}

	redis, err := Lookup("redis")
	if err != nil {
		t.Fatal(err)
	}
	app, svc = redis.ResolveAppService(map[string]string{"FLYNN_REDIS": "redis-abc"})
	if app != "redis-abc" || svc != "redis-abc" {
		t.Fatalf("redis %s %s", app, svc)
	}

	if _, err := Lookup("sqlite"); err == nil {
		t.Fatal("unknown provider must fail")
	}
	if !reflect.DeepEqual(KnownProviders(), []string{"postgres", "mysql", "mongodb", "redis", "kafka", "clickhouse"}) {
		t.Fatalf("providers %v", KnownProviders())
	}
}

func TestDefaultHostname(t *testing.T) {
	if got := DefaultHostname("postgres", "example.com"); got != "postgres.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := DefaultHostname("postgres.example.com", "example.com"); got != "postgres.example.com" {
		t.Fatalf("already qualified: %q", got)
	}
	if got := ClusterDomain("https://controller.demo.local", ""); got != "demo.local" {
		t.Fatalf("from URL: %q", got)
	}
	if got := ClusterDomain("https://controller.demo.local", "other.example"); got != "other.example" {
		t.Fatalf("explicit domain: %q", got)
	}
}

func TestNewTCPRouteDefaultsPassthrough(t *testing.T) {
	r, err := NewTCPRoute("postgres", "postgres.example.com", "", 0, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Type != "tcp" || r.Service != "postgres" || !r.Leader || r.TLSMode != router.TLSModePassthrough {
		t.Fatalf("%+v", r)
	}
	if r.ManagedCertificateDomain != nil {
		t.Fatal("passthrough must not request ACME")
	}
	term, err := NewTCPRoute("redis-abc", "redis-abc.example.com", "terminate", 3001, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if term.TLSMode != router.TLSModeTerminate || term.Port != 3001 {
		t.Fatalf("%+v", term)
	}
	if term.ManagedCertificateDomain == nil || *term.ManagedCertificateDomain != "redis-abc.example.com" {
		t.Fatalf("auto-tls should request managed cert: %+v", term.ManagedCertificateDomain)
	}
}

func TestFindTCPRoute(t *testing.T) {
	routes := []*router.Route{
		{Type: "http", Service: "postgres", Domain: "x"},
		{Type: "tcp", Service: "postgres", Domain: "postgres.example.com", Port: 3001},
		{Type: "tcp", Service: "mariadb", Domain: "mysql.example.com", Port: 3002},
	}
	got := FindTCPRoute(routes, "postgres", "postgres.example.com")
	if got == nil || got.Port != 3001 {
		t.Fatalf("%+v", got)
	}
	if FindTCPRoute(routes, "postgres", "other.example") != nil {
		t.Fatal("domain miss should be nil")
	}
	if got := FindTCPRoute(routes, "mariadb", ""); got == nil || got.Port != 3002 {
		t.Fatalf("fallback %+v", got)
	}
}

func TestFirewallCommands(t *testing.T) {
	if FirewallExposeCommand(3001) != "sudo flynn-host firewall:expose 3001" {
		t.Fatal(FirewallExposeCommand(3001))
	}
	if FirewallUnexposeCommand(3001) != "sudo flynn-host firewall:unexpose 3001" {
		t.Fatal(FirewallUnexposeCommand(3001))
	}
}
