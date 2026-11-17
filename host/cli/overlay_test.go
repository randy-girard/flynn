package cli

import (
	"os"
	"strings"
	"testing"
)

func TestOverlayDocs(t *testing.T) {
	checks := []struct {
		path string
		want []string
	}{
		{"../../docs/content/cli.md", []string{"overlay:set", "overlay:show", "FLANNEL_NETWORK"}},
		{"../../docs/content/production.html.md", []string{"overlay:set", "FLANNEL_SUBNET_LEN", "Re-run the same", "does not change flannel"}},
		{"../../docs/content/installation/manual.md", []string{"FLANNEL_NETWORK", "FLANNEL_SUBNET_LEN", "overlay:set"}},
		{"../../docs/content/architecture.html.md", []string{"overlay:set", "FLANNEL_NETWORK", "discoverd proxies"}},
	}
	for _, c := range checks {
		b, err := os.ReadFile(c.path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing %q", c.path, want)
			}
		}
	}
}

func TestOverlaySetParsesNetwork(t *testing.T) {
	parsed := parseHostCLI(t, "overlay:set", []string{"overlay:set", "--network", "100.64.0.0/10", "--subnet-len", "24"})
	if parsed.String["--network"] != "100.64.0.0/10" || parsed.String["--subnet-len"] != "24" {
		t.Fatalf("parsed: %#v", parsed.String)
	}
	parsed = parseHostCLI(t, "overlay:show", []string{"overlay:show"})
	if parsed == nil {
		t.Fatal("show did not parse")
	}
}
