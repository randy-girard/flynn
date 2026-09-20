package main

import (
	"errors"
	"os"
	"testing"

	"github.com/randy-girard/flynn/pkg/plugin"
)

var errCLITestsNoCluster = errors.New("cli tests do not use a live cluster")

func TestMain(m *testing.M) {
	fetchPluginCatalog = func() (*plugin.Catalog, error) {
		return nil, errCLITestsNoCluster
	}
	os.Exit(m.Run())
}

func TestCLITestsDoNotLoadALiveCluster(t *testing.T) {
	cat, err := clusterPluginCatalog()
	if cat != nil || !errors.Is(err, errCLITestsNoCluster) {
		t.Fatalf("got catalog=%v err=%v", cat, err)
	}
}
