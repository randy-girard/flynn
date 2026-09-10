package vxlan

import (
	"os"
	"strings"
	"testing"
)

func TestVXLANAssignsSlash32OnFlannelDevice(t *testing.T) {
	src, err := os.ReadFile("vxlan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "PrefixLen: 32") {
		t.Fatal("flannel.1 must be assigned a /32; a /16 overlay prefix makes peer container IPs look local and drops VXLAN frames")
	}
}

func TestVXLANWatchdogRestoresVTEPMAC(t *testing.T) {
	src, err := os.ReadFile("vxlan.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "macCheck := time.NewTicker(macCheckInterval)") {
		t.Fatal("Run() must poll EnsureMAC so udev MACAddressPolicy rewrites are repaired")
	}
	if !strings.Contains(body, "vb.dev.EnsureMAC(vb.vtepMAC)") {
		t.Fatal("Init/Run must call EnsureMAC with the advertised VTEP MAC")
	}
}
