package dbruntime

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCanManageAllowsHostAndClusterAdmin(t *testing.T) {
	if !CanManage(true, false) {
		t.Fatal("flynn-host")
	}
	if !CanManage(false, true) {
		t.Fatal("cluster admin")
	}
	if CanManage(false, false) {
		t.Fatal("tenant")
	}
}

func TestBuiltinsDifferByEngine(t *testing.T) {
	cat := BuiltinCatalog()
	if len(cat.Runtimes) != len(Engines)*3 {
		t.Fatalf("runtimes = %d", len(cat.Runtimes))
	}
	if cat.AllowCustomSizes {
		t.Fatal("custom sizes default off")
	}
	pg, ok := cat.Find(EnginePostgres, "small")
	if !ok {
		t.Fatal("postgres small")
	}
	rd, ok := cat.Find(EngineRedis, "small")
	if !ok {
		t.Fatal("redis small")
	}
	if rd.Disk >= pg.Disk {
		t.Fatalf("redis small disk %d is not smaller than postgres small disk %d", rd.Disk, pg.Disk)
	}
	seen := map[int64]string{}
	for _, eng := range Engines {
		rt, ok := cat.Find(eng, "small")
		if !ok || !rt.Builtin {
			t.Fatalf("%s small missing", eng)
		}
		if err := Validate(rt); err != nil {
			t.Fatal(err)
		}
		if other, ok := seen[rt.Disk]; ok {
			t.Fatalf("small disk %d shared by %s and %s", rt.Disk, other, eng)
		}
		seen[rt.Disk] = eng
		for _, name := range []string{"medium", "large"} {
			if _, ok := cat.Find(eng, name); !ok {
				t.Fatalf("missing %s %s", eng, name)
			}
		}
	}
}

func TestValidateRejectsBadFields(t *testing.T) {
	ok := Runtime{Name: "cache", Engine: EngineRedis, CPU: 100, Memory: 128, Disk: 256}
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	cases := []Runtime{
		{Name: "", Engine: EngineRedis, CPU: 1, Memory: 1, Disk: 1},
		{Name: "Bad", Engine: EngineRedis, CPU: 1, Memory: 1, Disk: 1},
		{Name: "cache", Engine: "oracle", CPU: 1, Memory: 1, Disk: 1},
		{Name: "cache", Engine: EngineRedis, CPU: 0, Memory: 1, Disk: 1},
		{Name: "cache", Engine: EngineRedis, CPU: 1, Memory: 0, Disk: 1},
		{Name: "cache", Engine: EngineRedis, CPU: 1, Memory: 1, Disk: 0},
	}
	for _, r := range cases {
		if err := Validate(r); err == nil {
			t.Fatalf("expected error for %+v", r)
		}
	}
	if _, err := NormalizeEngine("mysql"); err != nil {
		t.Fatal(err)
	}
	eng, _ := NormalizeEngine("mysql")
	if eng != EngineMariaDB {
		t.Fatalf("mysql -> %s", eng)
	}
}

func TestResolveDefaultAndUnpublished(t *testing.T) {
	cat := BuiltinCatalog()
	got, err := Resolve(EngineRedis, "", cat, false)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := cat.Find(EngineRedis, "small")
	if got != want.Size() {
		t.Fatalf("default %#v want %#v", got, want.Size())
	}
	got, err = Resolve("mysql", "medium", cat, false)
	if err != nil {
		t.Fatal(err)
	}
	med, _ := cat.Find(EngineMariaDB, "medium")
	if got != med.Size() {
		t.Fatalf("mysql medium %#v", got)
	}
	_, err = Resolve(EnginePostgres, "xlarge", cat, false)
	var unpublished *UnpublishedError
	if !errors.As(err, &unpublished) || unpublished.AllowCustom {
		t.Fatalf("unpublished: %v", err)
	}
	_, err = Resolve(EnginePostgres, "xlarge", cat, true)
	if !errors.As(err, &unpublished) || !unpublished.AllowCustom {
		t.Fatalf("unpublished with custom allowed: %v", err)
	}
}

func TestResolveProvisionStopsRawSizes(t *testing.T) {
	cat := BuiltinCatalog()
	custom := Size{CPU: 100, Memory: 128, Disk: 256}
	_, _, err := ResolveProvision(EngineRedis, "", custom, cat, false)
	if !errors.Is(err, ErrCustomSizesDisabled) {
		t.Fatalf("raw size: %v", err)
	}
	_, _, err = ResolveProvision(EngineRedis, "small", Size{CPU: 100}, cat, false)
	if !errors.Is(err, ErrCustomSizesDisabled) {
		t.Fatalf("partial raw: %v", err)
	}
	cat.AllowCustomSizes = true
	_, _, err = ResolveProvision(EngineRedis, "", Size{CPU: 100}, cat, true)
	if err == nil || errors.Is(err, ErrCustomSizesDisabled) {
		t.Fatalf("incomplete custom: %v", err)
	}
	sz, name, err := ResolveProvision(EngineRedis, "", custom, cat, true)
	if err != nil || name != "custom" || sz != custom {
		t.Fatalf("custom %#v %s %v", sz, name, err)
	}
	sz, name, err = ResolveProvision(EngineKafka, "", Size{}, cat, false)
	if err != nil || name != "small" {
		t.Fatalf("omit runtime %#v %s %v", sz, name, err)
	}
	k, _ := cat.Find(EngineKafka, "small")
	if sz != k.Size() {
		t.Fatalf("kafka small %#v", sz)
	}
}

func TestDefinitionUpdateLeavesInstanceSize(t *testing.T) {
	cat := BuiltinCatalog()
	provisioned, err := Resolve(EnginePostgres, "small", cat, false)
	if err != nil {
		t.Fatal(err)
	}
	disk := provisioned.Disk * 2
	updated, err := cat.Update(EnginePostgres, "small", UpdateFields{Disk: &disk})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Disk != disk {
		t.Fatalf("definition disk %d", updated.Disk)
	}
	if got := SizeAfterDefinitionUpdate(provisioned, updated); got != provisioned {
		t.Fatalf("instance resized to %#v", got)
	}
	again, _ := cat.Find(EnginePostgres, "small")
	if again.Disk != disk {
		t.Fatalf("catalog disk %d", again.Disk)
	}
	if err := cat.Remove(EnginePostgres, "small"); !errors.Is(err, ErrBuiltinRemove) {
		t.Fatalf("remove builtin: %v", err)
	}
}

func TestCustomRuntimeFileRoundTrip(t *testing.T) {
	if DefaultPath != "/etc/flynn/db-runtimes.json" {
		t.Fatal(DefaultPath)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.json")
	cat, err := Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Runtimes) != len(Engines)*3 || cat.AllowCustomSizes {
		t.Fatalf("missing file catalog %+v len %d", cat.AllowCustomSizes, len(cat.Runtimes))
	}
	if err := cat.Create(Runtime{Name: "cache", Engine: "mysql", CPU: 100, Memory: mib, Disk: gib}); err != nil {
		t.Fatal(err)
	}
	rt, ok := cat.Find(EngineMariaDB, "cache")
	if !ok || rt.Builtin || rt.Engine != EngineMariaDB {
		t.Fatalf("create %#v %v", rt, ok)
	}
	cat.AllowCustomSizes = true
	path := filepath.Join(dir, "nested", "db-runtimes.json")
	if err := Save(path, cat); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AllowCustomSizes {
		t.Fatal("allow custom not saved")
	}
	rt, ok = got.Find("mariadb", "cache")
	if !ok || rt.CPU != 100 || rt.Disk != gib {
		t.Fatalf("reloaded %#v", rt)
	}
	if err := got.Remove(EngineMariaDB, "cache"); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Find(EngineMariaDB, "cache"); ok {
		t.Fatal("cache still present")
	}
	if _, err := got.Update(EngineRedis, "small", UpdateFields{Name: strPtr("tiny")}); err == nil {
		t.Fatal("renamed builtin")
	}
}

func TestParseCPUAndBytes(t *testing.T) {
	cpu, err := ParseCPU("500m")
	if err != nil || cpu != 500 {
		t.Fatalf("cpu %d %v", cpu, err)
	}
	n, err := ParseBytes("10GB")
	if err != nil || n != 10*gib {
		t.Fatalf("disk %d %v", n, err)
	}
	if _, err := ParseBytes("0"); err == nil {
		t.Fatal("zero size")
	}
}

func strPtr(s string) *string { return &s }
