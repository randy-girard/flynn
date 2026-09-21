package scale

import (
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/inconshreveable/log15"
)

func TestAlreadyScaledFormationStillWaits(t *testing.T) {
	if !alreadyScaledStillWaits(3) {
		t.Fatal("a formation that already lists the database must wait for read-write")
	}
	if alreadyScaledStillWaits(0) {
		t.Fatal("a zero formation is scaled by this process, not treated as ready")
	}
}

func TestWaitForReadWriteRetriesUntilPrimaryIsWritable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var calls atomic.Int32
	go http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		n := calls.Add(1)
		writable := n >= 2
		fmt.Fprintf(w, `{"database":{"read_write":%t}}`, writable)
	}))

	httpPort := ln.Addr().(*net.TCPAddr).Port
	// Sirenia clients call port+1 for the status HTTP API.
	err = waitForReadWrite(fmt.Sprintf("127.0.0.1:%d", httpPort-1), log15.New())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 {
		t.Fatalf("status calls = %d, want at least 2", calls.Load())
	}
}
