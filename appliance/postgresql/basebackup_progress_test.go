package postgresql

import (
	"sync/atomic"
	"testing"

	"github.com/randy-girard/flynn/pkg/sirenia/client"
)

func TestParseBasebackupProgress(t *testing.T) {
	pr, ok := parseBasebackupProgress([]byte("  5242880/10485760 kB (50%), 0/1 tablespace (/data/db.basebackup/base/1/1259)"))
	if !ok {
		t.Fatal("expected a progress report")
	}
	if pr.DoneBytes != 5242880*1024 || pr.TotalBytes != 10485760*1024 || pr.Percent != 50 {
		t.Fatalf("got %+v", pr)
	}
	for _, line := range []string{
		"pg_basebackup: initiating base backup, waiting for checkpoint to complete",
		"pg_basebackup: write-ahead log end point: 5/2B227E08",
		"",
	} {
		if _, ok := parseBasebackupProgress([]byte(line)); ok {
			t.Fatalf("%q parsed as progress", line)
		}
	}
	if pr, _ := parseBasebackupProgress([]byte("110/100 kB (110%), 1/1 tablespace")); pr.Percent != 100 {
		t.Fatalf("percent not capped: %d", pr.Percent)
	}
}

func TestBasebackupProgressWriter(t *testing.T) {
	var dst atomic.Pointer[client.BasebackupProgress]
	w := &basebackupProgressWriter{dst: &dst}
	// Terminal output uses "\r"; split writes must still parse whole reports.
	w.Write([]byte("pg_basebackup: starting background WAL receiver\n10/100 kB (10%), 0/1 tab"))
	if dst.Load() != nil {
		t.Fatal("partial line must not be parsed")
	}
	w.Write([]byte("lespace\r40/100 kB (40%), 0/1 tablespace\r"))
	if got := dst.Load(); got == nil || got.Percent != 40 || got.DoneBytes != 40*1024 {
		t.Fatalf("got %+v", got)
	}
	w.Write([]byte("100/100 kB (100%), 1/1 tablespace\npg_basebackup: base backup completed\n"))
	if got := dst.Load(); got == nil || got.Percent != 100 {
		t.Fatalf("got %+v", got)
	}
}
