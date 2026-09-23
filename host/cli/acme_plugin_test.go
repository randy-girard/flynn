package cli

import (
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/plugin"
)

func TestLookupLetsEncryptPlugin(t *testing.T) {
	_, err := lookupLetsEncryptPlugin(nil)
	if err == nil || !strings.Contains(err.Error(), "plugin:install letsencrypt") {
		t.Fatalf("missing plugin: %v", err)
	}
	app := &ct.App{Name: "letsencrypt", Meta: map[string]string{plugin.MetaPlugin: "true"}}
	got, err := lookupLetsEncryptPlugin([]*ct.App{app})
	if err != nil || got.Name != "letsencrypt" {
		t.Fatalf("%v %v", got, err)
	}
	legacy := &ct.App{Name: "acme", Meta: map[string]string{"flynn-system-app": "true"}}
	got, err = lookupLetsEncryptPlugin([]*ct.App{legacy})
	if err != nil || got.Name != "acme" {
		t.Fatalf("legacy: %v %v", got, err)
	}
}
