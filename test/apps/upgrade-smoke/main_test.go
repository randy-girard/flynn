package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestBlobCountIncludesSeedFile(t *testing.T) {
	n := blobCount(dataFS)
	if n < 1 {
		t.Fatalf("expected at least the committed seed.txt, got %d", n)
	}
}

func TestResourceFlags(t *testing.T) {
	empty := resourceFlags(func(string) string { return "" })
	for name, ok := range empty {
		if ok {
			t.Fatalf("%s unexpectedly present with empty env", name)
		}
	}

	env := map[string]string{
		"FLYNN_POSTGRES":   "pg",
		"FLYNN_MYSQL":      "mysql",
		"FLYNN_MONGO":      "mongo",
		"FLYNN_REDIS":      "redis",
		"FLYNN_KAFKA":      "kafka",
		"FLYNN_CLICKHOUSE": "ch",
	}
	full := resourceFlags(func(k string) string { return env[k] })
	for name, ok := range full {
		if !ok {
			t.Fatalf("%s missing with full env", name)
		}
	}

	redisURL := resourceFlags(func(k string) string {
		if k == "REDIS_URL" {
			return "redis://leader.redis.discoverd:6379"
		}
		return ""
	})
	if !redisURL["redis"] {
		t.Fatal("redis should be detected from REDIS_URL")
	}
}

func TestBuildStatusReportsMissing(t *testing.T) {
	st := buildStatus(dataFS, func(string) string { return "" })
	if st.OK {
		t.Fatal("expected OK=false when no datastore env is set")
	}
	if st.BlobCount < 1 {
		t.Fatalf("blob_count=%d", st.BlobCount)
	}
	if st.Resources["postgres"] {
		t.Fatal("postgres should be false")
	}
}

func TestRootAndStatusHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		st := buildStatus(dataFS, os.Getenv)
		w.Header().Set("Content-Type", "application/json")
		if !st.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "ok\n" {
		t.Fatalf("GET / => %d %q", resp.StatusCode, body)
	}

	resp, err = http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /status without env: status %d", resp.StatusCode)
	}
	var st statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.OK {
		t.Fatal("status.ok should be false without datastore env")
	}
}

func TestBuildStatusOKWithAllDatastoreEnv(t *testing.T) {
	env := map[string]string{
		"FLYNN_POSTGRES":   "pg",
		"FLYNN_MYSQL":      "mysql",
		"FLYNN_MONGO":      "mongo",
		"FLYNN_REDIS":      "redis",
		"FLYNN_KAFKA":      "kafka",
		"FLYNN_CLICKHOUSE": "ch",
	}
	st := buildStatus(dataFS, func(k string) string { return env[k] })
	if !st.OK || st.Message != "ok" {
		t.Fatalf("status = %+v, want ok", st)
	}
	if st.BlobCount < 1 {
		t.Fatalf("blob_count=%d", st.BlobCount)
	}
	urlOnly := map[string]string{
		"DATABASE_URL":     "postgres://db",
		"FLYNN_MYSQL":      "mysql",
		"FLYNN_MONGO":      "mongo",
		"FLYNN_REDIS":      "redis",
		"FLYNN_KAFKA":      "kafka",
		"FLYNN_CLICKHOUSE": "ch",
	}
	if got := buildStatus(dataFS, func(k string) string { return urlOnly[k] }); !got.Resources["postgres"] || !got.OK {
		t.Fatalf("DATABASE_URL should satisfy postgres: %+v", got)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		out := buildStatus(dataFS, func(k string) string { return env[k] })
		w.Header().Set("Content-Type", "application/json")
		if !out.OK {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /status with all FLYNN_* env: status %d", resp.StatusCode)
	}
}
