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
	app, svc = s.ResolveAppService(map[string]string{"FLYNN_POSTGRES": "postgresql-canyon-28550"})
	if app != "postgresql-canyon-28550" || svc != "postgresql-canyon-28550" {
		t.Fatalf("isolated postgres %s %s", app, svc)
	}

	mysql, err := Lookup("mysql")
	if err != nil {
		t.Fatal(err)
	}
	app, svc = mysql.ResolveAppService(map[string]string{"FLYNN_MYSQL": "mysql-foo"})
	if app != "mysql-foo" || svc != "mysql-foo" {
		t.Fatalf("mysql tenant %s %s", app, svc)
	}
	app, svc = mysql.ResolveAppService(map[string]string{"FLYNN_MYSQL": "mariadb"})
	if app != "mariadb" || svc != "mariadb" {
		t.Fatalf("mysql legacy env %s %s", app, svc)
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
		{Type: "tcp", Service: "mysql", Domain: "mysql.example.com", Port: 3002},
	}
	got := FindTCPRoute(routes, "postgres", "postgres.example.com")
	if got == nil || got.Port != 3001 {
		t.Fatalf("%+v", got)
	}
	if FindTCPRoute(routes, "postgres", "other.example") != nil {
		t.Fatal("domain miss should be nil")
	}
	if got := FindTCPRoute(routes, "mysql", ""); got == nil || got.Port != 3002 {
		t.Fatalf("fallback %+v", got)
	}
}

func TestServiceForResourceUsesThatInstanceOnly(t *testing.T) {
	spec, service, err := ServiceForResource(map[string]string{
		"FLYNN_MYSQL":             "mysql-arroyo-64597",
		"MYSQL_HOST":              "leader.mysql-arroyo-64597.discoverd",
		"FLYNN_MYSQL_APRICOT_URL": "mysql://u:p@leader.mysql-arroyo-64597.discoverd:3306/db",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Provider != "mysql" || service != "mysql-arroyo-64597" || !spec.Leader {
		t.Fatalf("spec=%+v service=%s", spec, service)
	}
	if _, _, err := ServiceForResource(map[string]string{"FOO": "bar"}); err == nil {
		t.Fatal("non-datastore resource must fail")
	}
}

func TestFirewallFollowsRoute(t *testing.T) {
	if got := FirewallFollowsRoute(3306, true); got != "The cluster firewall will open TCP 3306." {
		t.Fatal(got)
	}
	if got := FirewallFollowsRoute(3306, false); got != "The cluster firewall will close TCP 3306." {
		t.Fatal(got)
	}
}

func TestHostAddrIP(t *testing.T) {
	if got := HostAddrIP("192.168.57.20:1113"); got != "192.168.57.20" {
		t.Fatal(got)
	}
	if got := HostAddrIP("http://192.168.57.21:1113"); got != "192.168.57.21" {
		t.Fatal(got)
	}
}

func TestExposedURLRewritesHostAndPort(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://app:pw@leader.postgresql-canyon-28550.discoverd:5432/db?sslmode=require",
	}
	got := ExposedURL(env, "192.168.57.20", 3001)
	want := "postgres://app:pw@192.168.57.20:3001/db?sslmode=require"
	if got != want {
		t.Fatalf("got %s", got)
	}
	redis := ExposedURL(map[string]string{
		"REDIS_URL": "redis://:secret@leader.redis-basin-45047.discoverd:6379",
	}, "192.168.57.21", 3004)
	if redis != "redis://:secret@192.168.57.21:3004" {
		t.Fatal(redis)
	}
	kafka := ExposedURL(map[string]string{"KAFKA_URL": "10.0.0.8:9092"}, "192.168.57.22", 3008)
	if kafka != "192.168.57.22:3008" {
		t.Fatal(kafka)
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
