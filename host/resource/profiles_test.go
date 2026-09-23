package resource

import (
	"testing"

	"github.com/docker/go-units"
	"github.com/randy-girard/flynn/pkg/typeconv"
)

func TestBuiltinProfilesMatchDefaults(t *testing.T) {
	medium, ok := ProfileByName("MEDIUM")
	if !ok {
		t.Fatal("medium profile missing")
	}
	def := Defaults()
	if *def[TypeMemory].Limit != medium.Memory {
		t.Fatalf("medium memory %d, default %d", medium.Memory, *def[TypeMemory].Limit)
	}
	if *def[TypeCPU].Limit != medium.CPU {
		t.Fatalf("medium cpu %d, default %d", medium.CPU, *def[TypeCPU].Limit)
	}

	small, _ := ProfileByName("small")
	large, _ := ProfileByName("large")
	if small.Memory != 512*units.MiB || small.CPU != 500 {
		t.Fatalf("small=%+v", small)
	}
	if large.Memory != 2*units.GiB || large.CPU != 2000 {
		t.Fatalf("large=%+v", large)
	}
	if !small.Builtin || !medium.Builtin || !large.Builtin {
		t.Fatal("builtin profiles must be marked builtin")
	}
}

func TestApplyNamedLimitsOverwritesMemoryAndCPU(t *testing.T) {
	r := Defaults()
	ApplyNamedLimits(r, 256*units.MiB, 250)
	if *r[TypeMemory].Limit != 256*units.MiB || *r[TypeMemory].Request != 0 {
		t.Fatalf("shared memory %+v", r[TypeMemory])
	}
	if *r[TypeCPU].Limit != 250 || *r[TypeCPU].Request != 0 {
		t.Fatalf("shared cpu %+v", r[TypeCPU])
	}
	ApplyNamedLimitsWithReserve(r, 512*units.MiB, 500, true)
	if *r[TypeMemory].Limit != 512*units.MiB || *r[TypeMemory].Request != 512*units.MiB {
		t.Fatalf("reserved memory %+v", r[TypeMemory])
	}
	if *r[TypeCPU].Limit != 500 || *r[TypeCPU].Request != 500 {
		t.Fatalf("reserved cpu %+v", r[TypeCPU])
	}
	if r[TypeTempDisk].Limit == nil {
		t.Fatal("temp_disk default must be preserved")
	}
	if r[TypeMaxProcs].Limit == nil || *r[TypeMaxProcs].Limit != DefaultPidsLimit {
		t.Fatalf("max_procs default must be preserved, got %+v", r[TypeMaxProcs])
	}
}

func TestValidateProfileName(t *testing.T) {
	if err := ValidateProfileName("small"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProfileName("  "); err == nil {
		t.Fatal("expected error")
	}
}

func TestProfileByNameUnknown(t *testing.T) {
	if _, ok := ProfileByName("xlarge"); ok {
		t.Fatal("xlarge is not builtin")
	}
}

func TestProcessRuntimeProfileDefaultsNewTypesToSmall(t *testing.T) {
	if got := ProcessRuntimeProfile("", nil); got != ProfileSmall {
		t.Fatalf("empty process = %q, want small", got)
	}
	if got := ProcessRuntimeProfile("  LARGE ", nil); got != "LARGE" {
		t.Fatalf("explicit profile = %q", got)
	}
	custom := Resources{TypeMemory: Spec{Limit: typeconv.Int64Ptr(256 * units.MiB)}}
	if got := ProcessRuntimeProfile("", custom); got != "" {
		t.Fatalf("custom memory should keep no named profile, got %q", got)
	}
	if HasMemoryOrCPU(nil) || HasMemoryOrCPU(Resources{}) {
		t.Fatal("empty resources are not explicit")
	}
}
