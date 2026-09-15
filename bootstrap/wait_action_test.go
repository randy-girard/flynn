package bootstrap

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWaitActionUnknownProtocol(t *testing.T) {
	err := (&WaitAction{URL: "ftp://example.invalid"}).Run(&State{})
	if err == nil || !strings.Contains(err.Error(), "unknown protocol") {
		t.Fatalf("got %v", err)
	}
}

func TestLookupDiscoverdURLHostSkipsExternalHosts(t *testing.T) {
	u, err := url.Parse("http://127.0.0.1:1/ping")
	if err != nil {
		t.Fatal(err)
	}
	if err := lookupDiscoverdURLHost(&State{}, u, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if u.Host != "127.0.0.1:1" {
		t.Fatalf("external host must be left alone: %s", u.Host)
	}
}

func TestHostAuthKeyFallsBackToEnv(t *testing.T) {
	s := &State{}
	t.Setenv("FLYNN_HOST_AUTH_KEY", "from-env")
	if s.HostAuthKey() != "from-env" {
		t.Fatalf("got %q", s.HostAuthKey())
	}
	s.SetHostAuthKey("from-state")
	if s.HostAuthKey() != "from-state" {
		t.Fatalf("state must win over env: %q", s.HostAuthKey())
	}
	_ = os.Unsetenv("FLYNN_HOST_AUTH_KEY")
}
