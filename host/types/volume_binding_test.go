package host

import "testing"

func TestValidateVolumesRejectsEmptyVolumeID(t *testing.T) {
	cfg := ContainerConfig{
		Volumes: []VolumeBinding{{Target: "/data", VolumeID: ""}},
	}
	if err := cfg.ValidateVolumes(); err == nil {
		t.Fatal("empty volume id must be rejected")
	}
}

func TestValidateVolumesRejectsWhitespaceVolumeID(t *testing.T) {
	cfg := ContainerConfig{
		Volumes: []VolumeBinding{{Target: "/data", VolumeID: "  "}},
	}
	if err := cfg.ValidateVolumes(); err == nil {
		t.Fatal("whitespace volume id must be rejected")
	}
}

func TestValidateVolumesAcceptsBoundVolume(t *testing.T) {
	cfg := ContainerConfig{
		Volumes: []VolumeBinding{{Target: "/data", VolumeID: "b43681a9-835e-41f5-80e1-30d457eeaca6"}},
	}
	if err := cfg.ValidateVolumes(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateVolumesAllowsNoVolumes(t *testing.T) {
	if err := (ContainerConfig{}).ValidateVolumes(); err != nil {
		t.Fatal(err)
	}
}
