package dbruntime

import (
	"errors"
	"testing"
)

func volumeTestCatalog(allowCustom bool) *Catalog {
	cat := &Catalog{AllowCustomSizes: allowCustom}
	if err := cat.EnsureEngine(EngineRedis); err != nil {
		panic(err)
	}
	return cat
}

func TestVolumeSizeCustomDisk(t *testing.T) {
	got, err := VolumeSize(volumeTestCatalog(true), EngineRedis, "custom", 5*gib)
	if err != nil {
		t.Fatal(err)
	}
	if got != 5*gib {
		t.Fatalf("size %d, want %d", got, 5*gib)
	}
}

func TestVolumeSizeUsesPublishedRuntimeDisk(t *testing.T) {
	cat := volumeTestCatalog(false)
	medium, _ := cat.Find(EngineRedis, "medium")
	for _, requested := range []int64{0, medium.Disk} {
		got, err := VolumeSize(cat, EngineRedis, "medium", requested)
		if err != nil {
			t.Fatalf("requested %d: %v", requested, err)
		}
		if got != medium.Disk {
			t.Fatalf("requested %d: size %d, want %d", requested, got, medium.Disk)
		}
	}
}

func TestVolumeSizeRejectsCustomDiskWhenDisabled(t *testing.T) {
	cat := volumeTestCatalog(false)
	for _, name := range []string{"custom", "small"} {
		_, err := VolumeSize(cat, EngineRedis, name, 5*gib)
		if !errors.Is(err, ErrCustomSizesDisabled) {
			t.Fatalf("%s: err %v, want ErrCustomSizesDisabled", name, err)
		}
	}
}

func TestVolumeSizeUnknownRuntimeWithoutDiskUsesHostDefault(t *testing.T) {
	got, err := VolumeSize(volumeTestCatalog(false), EngineRedis, "custom", 0)
	if err != nil || got != 0 {
		t.Fatalf("size %d err %v, want 0 nil", got, err)
	}
	got, err = VolumeSize(nil, EngineRedis, "small", 0)
	if err != nil || got != 0 {
		t.Fatalf("nil catalog: size %d err %v, want 0 nil", got, err)
	}
}

func TestVolumeSizeNeedsCatalogForRequestedDisk(t *testing.T) {
	if _, err := VolumeSize(nil, EngineRedis, "custom", 5*gib); err == nil {
		t.Fatal("requested disk without a catalog must fail")
	}
}

func TestVolumeSizeRejectsNegativeDisk(t *testing.T) {
	if _, err := VolumeSize(volumeTestCatalog(true), EngineRedis, "custom", -1); err == nil {
		t.Fatal("negative disk accepted")
	}
}
