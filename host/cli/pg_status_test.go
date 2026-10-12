package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	sireniaclient "github.com/randy-girard/flynn/pkg/sirenia/client"
	"github.com/randy-girard/flynn/pkg/sirenia/state"
)

func pgTestPeers(statuses map[string]*sireniaclient.Status) ([]*discoverd.Instance, func(string) (*sireniaclient.Status, error)) {
	var instances []*discoverd.Instance
	for _, addr := range []string{"10.0.0.3:5432", "10.0.0.2:5432", "10.0.0.1:5432", "10.0.0.4:5432"} {
		if _, ok := statuses[addr]; ok {
			instances = append(instances, &discoverd.Instance{Addr: addr})
		}
	}
	return instances, func(addr string) (*sireniaclient.Status, error) {
		if st := statuses[addr]; st != nil {
			return st, nil
		}
		return nil, errors.New("connection refused")
	}
}

func pgTestStatus(id string, role state.Role, db *sireniaclient.DatabaseInfo) *sireniaclient.Status {
	return &sireniaclient.Status{Peer: &state.PeerInfo{ID: id, Role: role}, Database: db}
}

func TestPgStatusRows(t *testing.T) {
	instances, status := pgTestPeers(map[string]*sireniaclient.Status{
		"10.0.0.1:5432": pgTestStatus("p", state.RolePrimary, &sireniaclient.DatabaseInfo{Running: true, ReadWrite: true, XLog: "1/00000000"}),
		"10.0.0.2:5432": pgTestStatus("s", state.RoleSync, &sireniaclient.DatabaseInfo{Running: true, XLog: "1/00000000"}),
		"10.0.0.3:5432": pgTestStatus("a", state.RoleAsync, &sireniaclient.DatabaseInfo{
			Basebackup: &sireniaclient.BasebackupProgress{DoneBytes: 1 << 30, TotalBytes: 4 << 30, Percent: 25},
		}),
		"10.0.0.4:5432": nil,
	})
	rows, ready := pgStatusRows(instances, status, nil)
	if ready {
		t.Fatal("basebackup and unreachable peers must not be ready")
	}
	var buf bytes.Buffer
	writePgStatus(&buf, rows)
	out := buf.String()
	for _, want := range []string{
		"10.0.0.1:5432  primary  read-write=true xlog=1/00000000",
		"10.0.0.2:5432  sync     caught up, 0 B behind",
		"10.0.0.3:5432  async    pg_basebackup 25% (1.0 GB of 4.0 GB)",
		"10.0.0.4:5432  unknown  unreachable: connection refused",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Index(out, "primary") > strings.Index(out, "sync") || strings.Index(out, "sync") > strings.Index(out, "async") {
		t.Fatalf("rows not ordered by role:\n%s", out)
	}
}

func TestPgStatusRowsNoPrimaryNotReady(t *testing.T) {
	instances, status := pgTestPeers(map[string]*sireniaclient.Status{
		"10.0.0.2:5432": pgTestStatus("s", state.RoleSync, &sireniaclient.DatabaseInfo{Running: true, XLog: "1/0"}),
	})
	if _, ready := pgStatusRows(instances, status, nil); ready {
		t.Fatal("no read-write primary must not be ready")
	}
}

func TestHostPgWaitReportsPercentUntilCaughtUp(t *testing.T) {
	asyncXLog := []string{"0/00000000", "0/02000000", "0/03000000", "0/04000000"}
	call := 0
	discover := func() ([]*discoverd.Instance, func(string) (*sireniaclient.Status, error), error) {
		x := asyncXLog[call]
		call++
		instances, status := pgTestPeers(map[string]*sireniaclient.Status{
			"10.0.0.1:5432": pgTestStatus("p", state.RolePrimary, &sireniaclient.DatabaseInfo{Running: true, ReadWrite: true, XLog: "0/04000000"}),
			"10.0.0.3:5432": pgTestStatus("a", state.RoleAsync, &sireniaclient.DatabaseInfo{Running: true, XLog: x}),
		})
		return instances, status, nil
	}
	var buf bytes.Buffer
	if err := hostPgWait(&buf, time.Minute, time.Millisecond, discover); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"catching up 0%, 64.0 MB behind", "catching up 50%, 32.0 MB behind", "caught up, 16.0 MB behind", "platform postgres replication is caught up"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if call != 3 {
		t.Fatalf("expected to stop on the third poll, got %d", call)
	}
}

func TestHostPgWaitTimeout(t *testing.T) {
	discover := func() ([]*discoverd.Instance, func(string) (*sireniaclient.Status, error), error) {
		return nil, nil, errors.New("discoverd down")
	}
	var buf bytes.Buffer
	err := hostPgWait(&buf, 5*time.Millisecond, time.Millisecond, discover)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(buf.String(), "discoverd down") {
		t.Fatalf("output %q", buf.String())
	}
}
