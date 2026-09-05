package updaterdeploy

import (
	"testing"

	"github.com/flynn/flynn/host/volume"
)

func TestIsTrackedAppVolume(t *testing.T) {
	if isTrackedAppVolume(nil) {
		t.Fatal("nil info should not be tracked")
	}
	if isTrackedAppVolume(&volume.Info{}) {
		t.Fatal("empty meta should not be tracked")
	}
	if isTrackedAppVolume(&volume.Info{Meta: map[string]string{"other": "x"}}) {
		t.Fatal("non-controller volume should not be tracked")
	}
	if !isTrackedAppVolume(&volume.Info{Meta: map[string]string{"flynn-controller.app": "app-id"}}) {
		t.Fatal("controller app volume should be tracked")
	}
}
