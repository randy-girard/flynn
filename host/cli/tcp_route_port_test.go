package cli

import (
	"strings"
	"testing"
)

func TestPluginRouteAddTCPRejectsPortOutsideWindow(t *testing.T) {
	for _, port := range []string{"22", "3001", "5002"} {
		args := parseHostCLI(t, "plugin:route", []string{
			"plugin:route", "dashboard", "add", "tcp", "-p", port,
		})
		err := runPluginRouteAddTCP(args, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "10000-29999") {
			t.Fatalf("port %s: got %v", port, err)
		}
	}
}
