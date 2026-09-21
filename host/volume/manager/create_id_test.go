package volumemanager

import (
	"testing"

	"github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/random"
)

func TestNewVolumeFromProviderRejectsNonUUID(t *testing.T) {
	m := New("", log15.New(), func() (volume.Provider, error) {
		return nil, nil
	})
	_, err := m.NewVolumeFromProvider("default", &volume.Info{ID: "../../x"})
	if err != volume.ErrInvalidVolumeID {
		t.Fatalf("path-escape id: err=%v, want ErrInvalidVolumeID", err)
	}
	_, err = m.NewVolumeFromProvider("default", &volume.Info{ID: "vol-1"})
	if err != volume.ErrInvalidVolumeID {
		t.Fatalf("short id: err=%v, want ErrInvalidVolumeID", err)
	}

	// UUID-like IDs pass validation; missing provider is a later error.
	_, err = m.NewVolumeFromProvider("default", &volume.Info{ID: random.UUID()})
	if err != ErrNoSuchProvider {
		t.Fatalf("valid UUID with no provider: err=%v, want ErrNoSuchProvider", err)
	}
	_, err = m.NewVolumeFromProvider("default", &volume.Info{})
	if err != ErrNoSuchProvider {
		t.Fatalf("empty id with no provider: err=%v, want ErrNoSuchProvider", err)
	}
	_, err = m.NewVolumeFromProvider("default", nil)
	if err != ErrNoSuchProvider {
		t.Fatalf("nil info with no provider: err=%v, want ErrNoSuchProvider", err)
	}
}
