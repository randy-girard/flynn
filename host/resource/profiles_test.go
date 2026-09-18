package resource

import (
	"testing"

	"github.com/docker/go-units"
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
	if small.Memory != 256*units.MiB || small.CPU != 250 {
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
	if *r[TypeMemory].Limit != 256*units.MiB || *r[TypeMemory].Request != 256*units.MiB {
		t.Fatalf("memory %+v", r[TypeMemory])
	}
	if *r[TypeCPU].Limit != 250 || *r[TypeCPU].Request != 250 {
		t.Fatalf("cpu %+v", r[TypeCPU])
	}
	if r[TypeTempDisk].Limit == nil {
		t.Fatal("temp_disk default must be preserved")
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
