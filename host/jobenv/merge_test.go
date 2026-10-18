package jobenv

import "testing"

func TestMergeKeepsHostDiscoverdWhenReleaseBlank(t *testing.T) {
	got := Merge(
		map[string]string{"DISCOVERD": "http://100.100.31.1:1111", "PATH": "/bin"},
		map[string]string{"DISCOVERD": "", "DISCOVERD_AUTH_KEY": ""},
		map[string]string{"DISCOVERD_AUTH_KEY": "host-key"},
	)
	if got["DISCOVERD"] != "http://100.100.31.1:1111" {
		t.Fatalf("blank release DISCOVERD cleared the host address: %q", got["DISCOVERD"])
	}
	if got["DISCOVERD_AUTH_KEY"] != "host-key" {
		t.Fatalf("blank release auth key cleared the host key: %q", got["DISCOVERD_AUTH_KEY"])
	}
	if got["PATH"] != "/bin" {
		t.Fatalf("PATH %q", got["PATH"])
	}
}

func TestMergeReleaseDiscoverdOverridesHost(t *testing.T) {
	got := Merge(
		map[string]string{"DISCOVERD": "http://100.100.31.1:1111"},
		map[string]string{"DISCOVERD": "http://100.100.70.1:1111"},
	)
	if got["DISCOVERD"] != "http://100.100.70.1:1111" {
		t.Fatalf("release DISCOVERD %q", got["DISCOVERD"])
	}
}
