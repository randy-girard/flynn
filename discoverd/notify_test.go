package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifyEmptyURLIsNoop(t *testing.T) {
	m := NewMain()
	m.Stdout = io.Discard
	m.logger = log.New(io.Discard, "", 0)
	done := make(chan struct{})
	go func() {
		m.Notify("", "192.0.2.200:53")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("empty notify URL must not retry")
	}
}

func TestNotifyRetriesUntilHostHTTPAccepts(t *testing.T) {
	origI, origT := notifyRetryInterval, notifyRetryTimeout
	notifyRetryInterval = 20 * time.Millisecond
	notifyRetryTimeout = 2 * time.Second
	t.Cleanup(func() {
		notifyRetryInterval, notifyRetryTimeout = origI, origT
	})

	var hits atomic.Int32
	var fails atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/host/discoverd" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if n := fails.Add(1); n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	m := NewMain()
	m.Stdout = io.Discard
	m.logger = log.New(io.Discard, "", 0)
	m.Notify(srv.URL+"/host/discoverd", "192.0.2.200:53")
	if hits.Load() < 1 {
		t.Fatal("notify must retry until flynn-host returns 2xx")
	}
}

func TestNotifyRetriesConnectionRefused(t *testing.T) {
	origI, origT := notifyRetryInterval, notifyRetryTimeout
	notifyRetryInterval = 20 * time.Millisecond
	notifyRetryTimeout = 2 * time.Second
	t.Cleanup(func() {
		notifyRetryInterval, notifyRetryTimeout = origI, origT
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	errc := make(chan error, 1)
	go func() {
		time.Sleep(80 * time.Millisecond)
		ln2, err := net.Listen("tcp", addr)
		if err != nil {
			errc <- err
			return
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/host/discoverd", func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		})
		errc <- http.Serve(ln2, mux)
	}()

	m := NewMain()
	m.Stdout = io.Discard
	m.logger = log.New(io.Discard, "", 0)
	m.Notify("http://"+addr+"/host/discoverd", "")
	if hits.Load() < 1 {
		t.Fatalf("notify must succeed after host HTTP starts listening (server err=%v)", <-errc)
	}
}

func TestNotifyGivesUpAfterTimeout(t *testing.T) {
	origI, origT := notifyRetryInterval, notifyRetryTimeout
	notifyRetryInterval = 10 * time.Millisecond
	notifyRetryTimeout = 50 * time.Millisecond
	t.Cleanup(func() {
		notifyRetryInterval, notifyRetryTimeout = origI, origT
	})

	m := NewMain()
	m.Stdout = io.Discard
	m.logger = log.New(io.Discard, "", 0)
	start := time.Now()
	m.Notify("http://127.0.0.1:1/host/discoverd", "")
	if time.Since(start) > time.Second {
		t.Fatal("notify must stop retrying after the timeout")
	}
}
