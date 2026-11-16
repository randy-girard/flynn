package acme

import (
	"testing"

	v1controller "github.com/randy-girard/flynn/controller/client/v1"
)

func TestNewControllerClientPrefersACMEToken(t *testing.T) {
	t.Setenv("ACME_TOKEN", "acme-token-value")
	t.Setenv("CONTROLLER_KEY", "cluster-god-key")
	c, err := newControllerClient(map[string]string{"AUTH_KEY": "from-meta"})
	if err != nil {
		t.Fatal(err)
	}
	vc, ok := c.(*v1controller.Client)
	if !ok {
		t.Fatalf("client type %T", c)
	}
	if vc.Token != "acme-token-value" || vc.Key != "" {
		t.Fatalf("token=%q key=%q", vc.Token, vc.Key)
	}
}
