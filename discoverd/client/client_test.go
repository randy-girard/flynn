package discoverd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSendsAuthKeyFromEnv(t *testing.T) {
	t.Setenv("DISCOVERD_AUTH_KEY", "from-env")
	var gotHeader, gotBasic string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Auth-Key")
		_, gotBasic, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	client := NewClientWithURL(srv.URL)
	var peers []string
	if err := client.Get("/raft/peers", &peers); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "from-env" {
		t.Fatalf("Auth-Key=%q", gotHeader)
	}
	if gotBasic != "from-env" {
		t.Fatalf("basic password=%q", gotBasic)
	}
}

func TestClientConfigAuthKeyOverridesEnv(t *testing.T) {
	t.Setenv("DISCOVERD_AUTH_KEY", "from-env")
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Auth-Key")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	client := NewClientWithConfig(Config{Endpoints: []string{srv.URL}, AuthKey: "from-config"})
	var peers []string
	if err := client.Get("/raft/peers", &peers); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "from-config" {
		t.Fatalf("Auth-Key=%q", gotHeader)
	}
}

func TestClientMaintainsHeadersOnRedirect(t *testing.T) {
	errc := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/services/test/leader", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			errc <- errors.New("Accept header was not forwarded")
			return
		}
		errc <- nil
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	rs := httptest.NewServer(http.RedirectHandler(ts.URL+"/services/test/leader", http.StatusFound))
	defer rs.Close()

	client := NewClientWithURL(rs.URL)

	leaders := make(chan *Instance)
	stream, err := client.Service("test").Leaders(leaders)
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	select {
	case err = <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("response timeout")
	}
}
