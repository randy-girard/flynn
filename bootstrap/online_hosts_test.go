package bootstrap

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	host "github.com/randy-girard/flynn/host/types"
)

func TestCheckOnlineHostsOmitsHostKeyOnStatusProbe(t *testing.T) {
	var sawAuth atomic.Bool
	var hostURL string
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/host/status" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			sawAuth.Store(true)
		}
		_ = json.NewEncoder(w).Encode(&host.HostStatus{ID: "h1", URL: hostURL})
	}))
	defer h.Close()
	hostURL = h.URL

	disc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/instances" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"url": h.URL}},
		})
	}))
	defer disc.Close()

	st := &State{ClusterURL: disc.URL, HostTimeout: 3 * time.Second}
	st.SetHostAuthKey("super-secret-host-key")
	if err := checkOnlineHosts(1, st, nil, st.HostTimeout); err != nil {
		t.Fatal(err)
	}
	if sawAuth.Load() {
		t.Fatal("GetStatus probe must not send the host key")
	}
	if len(st.Hosts) != 1 || st.Hosts[0].ID() != "h1" {
		t.Fatalf("hosts=%v", st.Hosts)
	}
}

func TestCheckOnlineHostsIgnoresDiscoveryURLsOutsidePeerList(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Error("attacker must not receive host auth")
		}
		_ = json.NewEncoder(w).Encode(&host.HostStatus{ID: "evil", URL: "http://evil.example:1113"})
	}))
	defer attacker.Close()

	var peerURL string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(&host.HostStatus{ID: "peer", URL: peerURL})
	}))
	defer peer.Close()
	peerURL = peer.URL

	disc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("discovery must not replace a configured peer list")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"url": attacker.URL}, {"url": peer.URL}},
		})
	}))
	defer disc.Close()

	st := &State{ClusterURL: disc.URL, HostTimeout: 3 * time.Second}
	st.SetHostAuthKey("super-secret-host-key")
	if err := checkOnlineHosts(1, st, []string{peer.URL}, st.HostTimeout); err != nil {
		t.Fatal(err)
	}
	if attackerHits.Load() != 0 {
		t.Fatalf("attacker hits=%d", attackerHits.Load())
	}
	if len(st.Hosts) != 1 || st.Hosts[0].ID() != "peer" {
		t.Fatalf("hosts=%v", st.Hosts)
	}
}

func TestCheckOnlineHostsSkipsStatusURLHostMismatch(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(&host.HostStatus{ID: "h1", URL: "http://evil.example:1113"})
	}))
	defer h.Close()

	st := &State{HostTimeout: 2 * time.Second}
	st.SetHostAuthKey("super-secret-host-key")
	err := checkOnlineHosts(1, st, []string{h.URL}, st.HostTimeout)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("mismatch must not join, err=%v", err)
	}
	if len(st.Hosts) != 0 {
		t.Fatalf("hosts=%v", st.Hosts)
	}
}

func TestDiscoveryHostsMatch(t *testing.T) {
	if !discoveryHostsMatch("http://10.0.0.1:1113", "http://10.0.0.1:1113/unused") {
		t.Fatal("same host")
	}
	if discoveryHostsMatch("http://10.0.0.1:1113", "http://10.0.0.2:1113") {
		t.Fatal("different host")
	}
	if !urlInPeerList("http://10.0.0.1:1113", []string{"http://10.0.0.1:1113", "http://10.0.0.2:1113"}) {
		t.Fatal("in list")
	}
	if urlInPeerList("http://8.8.8.8:1113", []string{"http://10.0.0.1:1113"}) {
		t.Fatal("not in list")
	}
}
