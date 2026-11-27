package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/host/volume"
	"github.com/randy-girard/flynn/pkg/cluster"
)

// flynn-host volume:list shows each volume's capacity (the ZFS refquota the
// host gave it, NEW-28); a legacy volume created without one shows "-".
func TestWriteVolumeListShowsSize(t *testing.T) {
	h := cluster.NewHost("h1", "10.0.0.1:1113", nil, nil)
	var out bytes.Buffer
	writeVolumeList(&out, sortVolumes{
		{Host: h, Volume: &volume.Info{ID: "v-sized", Type: volume.VolumeTypeData, CreatedAt: time.Now(), Size: volume.DefaultSize}},
		{Host: h, Volume: &volume.Info{ID: "v-legacy", Type: volume.VolumeTypeData, CreatedAt: time.Now()}},
	})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "SIZE") {
		t.Fatalf("output:\n%s\nwant a SIZE column and two rows", out.String())
	}
	if !strings.Contains(lines[1], "v-sized") || !strings.Contains(lines[1], "20 GiB") {
		t.Fatalf("sized row %q, want 20 GiB", lines[1])
	}
	if !strings.Contains(lines[2], "v-legacy") || !strings.Contains(lines[2], " - ") {
		t.Fatalf("legacy row %q, want -", lines[2])
	}
}
