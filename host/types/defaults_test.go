//go:build linux

package host

import "testing"

func TestUserJobCapabilitiesOmitAuditAndFileCaps(t *testing.T) {
	keep := map[string]bool{}
	for _, c := range UserJobCapabilities {
		keep[c] = true
	}
	for _, c := range []string{"CAP_SETFCAP", "CAP_SETPCAP", "CAP_AUDIT_READ", "CAP_AUDIT_WRITE"} {
		if keep[c] {
			t.Fatalf("user jobs must not have %s in the bounding set", c)
		}
	}
	for _, c := range []string{"CAP_SETUID", "CAP_SETGID", "CAP_DAC_OVERRIDE", "CAP_KILL"} {
		if !keep[c] {
			t.Fatalf("user jobs still need %s for containerinit to drop privileges", c)
		}
	}
}

func TestDefaultAutoCreatedDevicesHavePaths(t *testing.T) {
	if len(DefaultAutoCreatedDevices) == 0 {
		t.Fatal("expected default device nodes")
	}
	for _, d := range DefaultAutoCreatedDevices {
		if d.Path == "" {
			t.Fatalf("auto-created device %#v has empty path (runc userns bind-mounts EISDIR)", d)
		}
		if d.Path[0] != '/' {
			t.Fatalf("auto-created device path %q must be absolute", d.Path)
		}
	}
}
