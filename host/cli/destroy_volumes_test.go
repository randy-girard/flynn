package cli

import (
	"errors"
	"testing"

	"github.com/randy-girard/flynn/host/volume"
)

type stubVolume struct {
	info volume.Info
}

func (s *stubVolume) Info() *volume.Info        { return &s.info }
func (s *stubVolume) Provider() volume.Provider { return nil }
func (s *stubVolume) Location() string          { return "" }
func (s *stubVolume) IsSnapshot() bool          { return false }

func TestSkipBusyImageLayer(t *testing.T) {
	busy := errors.New(`exit status 1: "/usr/sbin/zfs zfs destroy -f flynn-default/ext2/host0-x" => cannot destroy 'flynn-default/ext2/host0-x': dataset is busy`)
	ext2 := &stubVolume{info: volume.Info{ID: "host0-x", Type: volume.VolumeTypeExt2}}
	if !skipBusyImageLayer(ext2, busy) {
		t.Fatal("busy ext2 layer must not fail start-all destroy-volumes")
	}
	squash := &stubVolume{info: volume.Info{ID: "layer", Type: volume.VolumeTypeSquashfs}}
	if !skipBusyImageLayer(squash, busy) {
		t.Fatal("busy squashfs layer must skip")
	}
	data := &stubVolume{info: volume.Info{ID: "data", Type: volume.VolumeTypeData}}
	if skipBusyImageLayer(data, busy) {
		t.Fatal("busy data volume must still fail destroy-volumes")
	}
	if skipBusyImageLayer(ext2, errors.New("other")) {
		t.Fatal("non-busy error")
	}
}
