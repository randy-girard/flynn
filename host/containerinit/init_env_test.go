//go:build linux

package containerinit

import (
	"sort"
	"strings"
	"testing"
)

func TestChildEnvHidesDiscoverdForUserJobs(t *testing.T) {
	got := childEnv(map[string]string{
		"PATH":               "/bin",
		"DISCOVERD":          "http://50.116.33.9:1111",
		"DISCOVERD_AUTH_KEY": "disc-secret",
		HideDiscoverdEnv:     "1",
		"CURSOR_API_KEY":     "keep-me",
		"EXTERNAL_IP":        "100.64.0.10",
	})
	sort.Strings(got)
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "DISCOVERD=") || strings.Contains(joined, "DISCOVERD_AUTH_KEY=") || strings.Contains(joined, HideDiscoverdEnv) {
		t.Fatalf("user child env leaked discoverd: %v", got)
	}
	want := []string{"CURSOR_API_KEY=keep-me", "EXTERNAL_IP=100.64.0.10", "PATH=/bin"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestChildEnvHidesDiscoverdWhenHostRegisters(t *testing.T) {
	got := childEnv(map[string]string{
		"DISCOVERD":           "http://100.64.57.1:1111",
		"DISCOVERD_AUTH_KEY":  "disc-secret",
		HostRegistersServices: "1",
		"PATH":                "/bin",
	})
	sort.Strings(got)
	joined := strings.Join(got, ",")
	if strings.Contains(joined, "DISCOVERD=") || strings.Contains(joined, "DISCOVERD_AUTH_KEY=") || strings.Contains(joined, HostRegistersServices) {
		t.Fatalf("host-registered jobs must not leak discoverd: %v", got)
	}
}

func TestChildEnvKeepsDiscoverdForSystemJobs(t *testing.T) {
	got := childEnv(map[string]string{
		"DISCOVERD":          "http://50.116.33.9:1111",
		"DISCOVERD_AUTH_KEY": "disc-secret",
		"PATH":               "/bin",
	})
	sort.Strings(got)
	if strings.Join(got, ",") != "DISCOVERD=http://50.116.33.9:1111,DISCOVERD_AUTH_KEY=disc-secret,PATH=/bin" {
		t.Fatalf("system jobs must keep DISCOVERD and DISCOVERD_AUTH_KEY: %v", got)
	}
}

func TestDiscoverdClientFromEnvSetsAuthKey(t *testing.T) {
	t.Setenv("DISCOVERD_AUTH_KEY", "")
	c := discoverdClientFromEnv(map[string]string{
		"DISCOVERD":          "http://127.0.0.1:1111",
		"DISCOVERD_AUTH_KEY": "from-job-env",
	})
	if c.Key != "from-job-env" {
		t.Fatalf("Key=%q, want from-job-env (containerinit os.Environ does not carry job env)", c.Key)
	}
}
