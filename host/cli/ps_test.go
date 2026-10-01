package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/randy-girard/flynn/host/types"
)

func TestPrintJobsShowsDisplayNameWhenMetadataNameMissing(t *testing.T) {
	var buf bytes.Buffer
	printJobs(sortJobs{
		{
			Status: host.StatusRunning,
			Job: &host.Job{
				ID: "devnode1-97100ccb-1b45-4af1-9c71-78e1d3571042",
				Metadata: map[string]string{
					"flynn-controller.app_name": "postgres",
					"flynn-controller.type":     "postgres",
				},
			},
		},
		{
			Status: host.StatusRunning,
			Job: &host.Job{
				ID: "devnode2-named",
				Metadata: map[string]string{
					"flynn-controller.app_name": "status",
					"flynn-controller.type":     "web",
					host.MetaControllerName:     "web.5600",
				},
			},
		},
	}, &buf)
	out := buf.String()
	if !strings.Contains(out, "postgres.8676") {
		t.Fatalf("bootstrap job missing display name:\n%s", out)
	}
	if !strings.Contains(out, "web.5600") {
		t.Fatalf("allocated name missing:\n%s", out)
	}
}
