package plugin

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

func TestOfficialDatastoreAddonContracts(t *testing.T) {
	cat := dbruntime.BuiltinCatalog()
	pg := OfficialAddon("postgres")
	if pg == nil || !pg.Supports(VerbExpose) || pg.Env.Prefix != "FLYNN_POSTGRESQL_" {
		t.Fatalf("postgres addon %#v", pg)
	}
	if !strings.Contains(pg.CreateUsage(), "--as <name>") || !strings.Contains(pg.CreateUsage(), "--follow") {
		t.Fatalf("usage %s", pg.CreateUsage())
	}
	raw, err := pg.ProvisionConfig(CreateInput{}, cat)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	small, _ := cat.Find(dbruntime.EnginePostgres, "small")
	if body["runtime"] != "small" || int64(body["disk"].(float64)) != small.Disk {
		t.Fatalf("default %#v", body)
	}
	raw, err = pg.ProvisionConfig(CreateInput{As: "ANALYTICS", Follow: "leader", Runtime: "medium", Replication: "logical", AutoFailover: true}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"as":"ANALYTICS"`) || !strings.Contains(string(raw), `"replication":"logical"`) || !strings.Contains(string(raw), `"auto_failover":true`) {
		t.Fatalf("passthrough %s", raw)
	}
	if err := pg.CheckArgv([]string{"--auto-failover"}); err == nil {
		t.Fatal("auto-failover without follow")
	}
	for _, typ := range []string{"mongodb", "kafka", "clickhouse"} {
		spec := OfficialAddon(typ)
		if spec == nil {
			t.Fatal(typ)
		}
		err := spec.CheckArgv([]string{"addons:create", typ, "--follow", typ + "-harbor-11111"})
		if err == nil || !strings.Contains(err.Error(), "flynn "+typ+":scale") {
			t.Fatalf("%s: %v", typ, err)
		}
	}
	redis := OfficialAddon("redis")
	_, err = redis.ProvisionConfig(CreateInput{CPU: "100", Memory: "128MB", Disk: "1GB"}, cat)
	if !errors.Is(err, dbruntime.ErrCustomSizesDisabled) {
		t.Fatalf("raw size: %v", err)
	}
	cat.AllowCustomSizes = true
	raw, err = redis.ProvisionConfig(CreateInput{CPU: "100", Memory: "128MB", Disk: "1GB"}, cat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"runtime":"custom"`) {
		t.Fatalf("custom %s", raw)
	}
	sched := OfficialAddon("scheduler")
	if sched == nil || sched.Backend != BackendPlugin || sched.Supports(VerbAttach) || strings.Contains(sched.CreateUsage(), "--as") {
		t.Fatalf("scheduler %#v %s", sched, sched.CreateUsage())
	}
	if sched.VerbError(VerbAttach).Error() != "scheduler addons do not support attach" {
		t.Fatal(sched.VerbError(VerbAttach))
	}
}

func TestAddonFromAppPrefersMeta(t *testing.T) {
	spec := &AddonSpec{
		Type: "widget", Description: "A widget", Verbs: []string{VerbCreate, VerbDestroy},
		Backend: BackendPlugin, API: "http://widget.discoverd",
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	app := &ct.App{Name: "widget-plugin", Meta: map[string]string{
		MetaPlugin: "true", MetaPluginKind: KindApp, MetaPluginAddon: string(raw),
	}}
	got := AddonFromApp(app)
	if got == nil || got.Type != "widget" || got.App != "widget-plugin" {
		t.Fatalf("%#v", got)
	}
}

func TestMariaDBAlias(t *testing.T) {
	spec := OfficialAddon("mariadb")
	if spec == nil || spec.Type != "mysql" {
		t.Fatalf("%#v", spec)
	}
}
