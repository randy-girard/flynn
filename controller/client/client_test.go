package controller

import (
	"strings"
	"testing"

	v1controller "github.com/flynn/flynn/controller/client/v1"
)

func TestNewClientDefaultsAndToken(t *testing.T) {
	c, err := NewClient("", "cluster-key")
	if err != nil {
		t.Fatal(err)
	}
	v1, ok := c.(*v1controller.Client)
	if !ok {
		t.Fatalf("%T", c)
	}
	if v1.URL != "http://controller.discoverd" || v1.Key != "cluster-key" || v1.Token != "" {
		t.Fatalf("key client %+v", v1.Client)
	}

	tok, err := NewClientWithToken("https://controller.example", "scoped-jwt")
	if err != nil {
		t.Fatal(err)
	}
	v1 = tok.(*v1controller.Client)
	if v1.Token != "scoped-jwt" || v1.Key != "" {
		t.Fatalf("token client Key=%q Token=%q", v1.Key, v1.Token)
	}

	pinned, err := NewClientWithConfig("https://controller.example", "cluster-key", Config{Pin: []byte("pin-bytes"), Domain: "controller.example"})
	if err != nil {
		t.Fatal(err)
	}
	v1 = pinned.(*v1controller.Client)
	if v1.Host != "controller.example" || v1.HijackDial == nil {
		t.Fatalf("pinned %+v", v1.Client)
	}

	unpinned, err := NewClientWithConfig("https://controller.example", "cluster-key", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if unpinned.(*v1controller.Client).HijackDial != nil {
		t.Fatal("unpinned client should use the default transport")
	}

	if _, err := NewClient("http://%", "k"); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("invalid URI: %v", err)
	}
}
