package instanceport

import (
	"reflect"
	"strconv"
	"testing"
)

func TestTwoInstancesGetDifferentPorts(t *testing.T) {
	p1, err := Allocate(nil)
	if err != nil {
		t.Fatal(err)
	}
	used := map[int]string{p1: "a"}
	p2, err := Allocate(used)
	if err != nil {
		t.Fatal(err)
	}
	if p1 == p2 {
		t.Fatalf("both instances got port %d", p1)
	}
	if p1 < MinPort || p1 > MaxPort || p2 < MinPort || p2 > MaxPort {
		t.Fatalf("ports %d %d outside %d-%d", p1, p2, MinPort, MaxPort)
	}
	if err := Claim(p1, "b", used); err != ErrCollision {
		t.Fatalf("collision: %v", err)
	}
	if err := Claim(p1, "a", used); err != nil {
		t.Fatalf("owner may keep its port: %v", err)
	}
	if _, err := AllocateRange(map[int]string{4000: "a"}, 4000, 4000); err != ErrExhausted {
		t.Fatalf("exhausted range: %v", err)
	}
}

func TestPortOpenOnlyOnHostRunningTheJob(t *testing.T) {
	jobs := []Job{
		{InstanceID: "a", Port: 3100, Host: "h1"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}
	if got := PortsForHost("h1", jobs); !reflect.DeepEqual(got, []int{3100}) {
		t.Fatalf("h1 ports %v", got)
	}
	if got := PortsForHost("h2", jobs); !reflect.DeepEqual(got, []int{3101}) {
		t.Fatalf("h2 ports %v", got)
	}
	if got := PortsForHost("h3", jobs); len(got) != 0 {
		t.Fatalf("idle host %v", got)
	}
}

func TestFailoverOpensNewHostAndClosesOld(t *testing.T) {
	before := Desired([]Job{
		{InstanceID: "a", Port: 3100, Host: "h1"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	})
	after := Desired([]Job{
		{InstanceID: "a", Port: 3100, Host: "h2"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	})
	open, close := Diff(before, after)
	wantOpen := []Exposure{{InstanceID: "a", Port: 3100, Host: "h2"}}
	wantClose := []Exposure{{InstanceID: "a", Port: 3100, Host: "h1"}}
	if !reflect.DeepEqual(open, wantOpen) || !reflect.DeepEqual(close, wantClose) {
		t.Fatalf("open=%+v close=%+v", open, close)
	}
	if got := PortsForHost("h2", []Job{
		{InstanceID: "a", Port: 3100, Host: "h2"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}); !reflect.DeepEqual(got, []int{3100, 3101}) {
		t.Fatalf("new host ports %v", got)
	}
	if got := PortsForHost("h1", []Job{
		{InstanceID: "a", Port: 3100, Host: "h2"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}); len(got) != 0 {
		t.Fatalf("old host still open %v", got)
	}
}

func TestPlanForADoesNotListBPort(t *testing.T) {
	jobs := []Job{
		{InstanceID: "a", Port: 3100, Host: "h1"},
		{InstanceID: "b", Port: 3101, Host: "h2"},
	}
	plans, err := Plans(jobs)
	if err != nil {
		t.Fatal(err)
	}
	var planA InstancePlan
	for _, p := range plans {
		if p.InstanceID == "a" {
			planA = p
		}
	}
	if planA.InstanceID != "a" || planA.Port != 3100 {
		t.Fatalf("plan A %+v", planA)
	}
	for _, port := range planA.ExposePorts() {
		if port == 3101 {
			t.Fatalf("plan for A lists B's port: %+v", planA)
		}
	}
	for _, e := range ForInstance("a", Desired(jobs)) {
		if e.Port != 3100 || e.InstanceID != "a" {
			t.Fatalf("exposure for A includes another instance: %+v", e)
		}
	}
	if !reflect.DeepEqual(planA.Hosts, []string{"h1"}) {
		t.Fatalf("hosts %+v", planA.Hosts)
	}
}

func TestAssignEnvStoresPort(t *testing.T) {
	if !IsDatastore("mysql") || !IsDatastore("mariadb") || IsDatastore("sqlite") {
		t.Fatal("datastore set")
	}
	env, err := AssignEnv("inst-a", map[string]string{
		"DATABASE_URL": "postgres://u:p@leader.db.discoverd:5432/app",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1, ok := ParsePort(env[EnvPort])
	if !ok || env[EnvID] != "inst-a" {
		t.Fatalf("env %+v", env)
	}
	if env["DATABASE_URL"] != "postgres://u:p@leader.db.discoverd:5432/app" {
		t.Fatalf("url %s", env["DATABASE_URL"])
	}
	used := map[int]string{p1: "inst-a"}
	env2, err := AssignEnv("inst-b", nil, used)
	if err != nil {
		t.Fatal(err)
	}
	p2, ok := ParsePort(env2[EnvPort])
	if !ok || p2 == p1 {
		t.Fatalf("second port %d", p2)
	}
	if _, err := AssignEnv("inst-b", map[string]string{EnvPort: strconv.Itoa(p1)}, used); err != ErrCollision {
		t.Fatalf("collision: %v", err)
	}
}

func TestParseJobIgnoresClientEnv(t *testing.T) {
	if _, ok := ParseJob("h1", nil, map[string]string{EnvPort: "3100", EnvID: "a"}); ok {
		t.Fatal("client env must not open a host port")
	}
	job, ok := ParseJob("h1", map[string]string{MetaInstanceID: "a", MetaPort: "3100"}, nil)
	if !ok || job.InstanceID != "a" || job.Port != 3100 || job.Host != "h1" {
		t.Fatalf("meta job %+v %v", job, ok)
	}
}

func TestStampEnvKeepsDiscoverdURL(t *testing.T) {
	env := StampEnv(map[string]string{
		"DATABASE_URL": "postgres://u:p@leader.abc.discoverd:5432/db?sslmode=require",
		"REDIS_URL":    "redis://db.example.com:6379/0",
	}, "inst-a", 3100)
	if env[EnvPort] != "3100" || env[EnvID] != "inst-a" {
		t.Fatalf("env %+v", env)
	}
	if env["DATABASE_URL"] != "postgres://u:p@leader.abc.discoverd:5432/db?sslmode=require" {
		t.Fatalf("discoverd URL changed: %s", env["DATABASE_URL"])
	}
	if env["REDIS_URL"] != "redis://db.example.com:3100/0" {
		t.Fatalf("external URL %s", env["REDIS_URL"])
	}
}
