//go:build ignore

// Deploy a missing optional resource system app (kafka, clickhouse) on an
// existing cluster using /etc/flynn/images.json.
//
// Usage: KEY=<controller-key> NAME=clickhouse go run script/deploy-optional-resource.go

package main

import (
	"encoding/json"
	"fmt"
	"os"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/updaterdeploy"
	"github.com/inconshreveable/log15"
)

func main() {
	name := os.Getenv("NAME")
	if name == "" {
		fmt.Fprintln(os.Stderr, "NAME env required (kafka or clickhouse)")
		os.Exit(1)
	}
	key := os.Getenv("KEY")
	if key == "" {
		fmt.Fprintln(os.Stderr, "KEY env required")
		os.Exit(1)
	}
	controllerURL := os.Getenv("CONTROLLER_URL")
	if controllerURL == "" {
		controllerURL = "http://100.100.52.47"
	}

	client, err := controller.NewClient(controllerURL, key)
	if err != nil {
		panic(err)
	}

	var images map[string]*ct.Artifact
	f, err := os.Open("/etc/flynn/images.json")
	if err != nil {
		panic(err)
	}
	if err := json.NewDecoder(f).Decode(&images); err != nil {
		panic(err)
	}
	img := images[name]
	if img == nil {
		panic(fmt.Sprintf("no %s image in /etc/flynn/images.json", name))
	}

	if err := updaterdeploy.EnsureOptionalResourceApp(client, name, img, log15.New()); err != nil {
		panic(err)
	}
	fmt.Println(name, "deployed successfully")
}
