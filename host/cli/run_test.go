package cli

import (
	"testing"

	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/netpolicy"
)

func TestOperatorRunJobIsHostTrusted(t *testing.T) {
	job := &host.Job{
		Config: host.ContainerConfig{
			Mounts: []host.Mount{{
				Target:   "/src",
				Location: "/go/src/github.com/flynn/flynn",
			}},
		},
	}
	markOperatorJob(job)
	if !netpolicy.HostTrusted(job) {
		t.Fatal("flynn-host run must be host-trusted so --bind is kept")
	}
	if job.Partition != "system" || job.Metadata["flynn-system-app"] != "true" {
		t.Fatalf("operator job partition=%q metadata=%v", job.Partition, job.Metadata)
	}
}
