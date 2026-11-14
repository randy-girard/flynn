package host

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLivenessStatusOmitsJobIDsAndSubnet(t *testing.T) {
	full := &HostStatus{
		ID:       "host-1",
		URL:      "http://10.0.0.8:1113",
		PID:      42,
		Auth:     true,
		Version:  "v2026.1",
		Flags:    []string{"tls"},
		Tags:     map[string]string{"disk": "ssd"},
		LogDrops: 7,
		Discoverd: &DiscoverdConfig{
			JobID: "discoverd-job-9f3a",
			URL:   "http://10.0.0.8:1111",
			DNS:   "10.0.0.8",
		},
		Network: &NetworkConfig{
			JobID:     "flannel-job-9f3a",
			Subnet:    "100.64.9.0/24",
			MTU:       1450,
			Resolvers: []string{"10.0.0.8"},
		},
	}

	got := full.Liveness()
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, leak := range []string{
		"discoverd-job-9f3a",
		"flannel-job-9f3a",
		"100.64.9.0/24",
		"10.0.0.8:1111",
		`"discoverd"`,
		`"network"`,
	} {
		if strings.Contains(body, leak) {
			t.Fatalf("unauthenticated status leaked %q in %s", leak, body)
		}
	}
	if got.ID != "host-1" || got.URL != "http://10.0.0.8:1113" || !got.Auth || got.Version != "v2026.1" || got.LogDrops != 7 {
		t.Fatalf("liveness fields dropped: %+v", got)
	}
	if !strings.Contains(body, `"log_drops":7`) {
		t.Fatalf("liveness omitted log_drops: %s", body)
	}
	if full.Discoverd == nil || full.Discoverd.JobID != "discoverd-job-9f3a" || full.Network == nil || full.Network.Subnet != "100.64.9.0/24" {
		t.Fatal("liveness mutated the stored status")
	}
}
