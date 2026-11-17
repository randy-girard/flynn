package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/overlay"
)

type Config struct {
	Network   string
	SubnetMin string `json:",omitempty"`
	SubnetMax string `json:",omitempty"`
	SubnetLen uint   `json:",omitempty"`
	Backend   struct {
		Type string
		VNI  uint `json:",omitempty"`
		Port uint `json:",omitempty"`
	}
}

const serviceName = "flannel"

func main() {
	var config Config
	config.Backend.Type = "vxlan"
	if backend := os.Getenv("BACKEND"); backend != "" {
		config.Backend.Type = backend
	}
	config.Network = os.Getenv("NETWORK")
	if config.Network == "" {
		config.Network = "100.100.0.0/16"
	}
	flag.StringVar(&config.SubnetMin, "subnet-min", "", "container network min subnet")
	flag.StringVar(&config.SubnetMax, "subnet-max", "", "container network max subnet")
	flag.UintVar(&config.SubnetLen, "subnet-len", 0, "container network subnet length")
	flag.UintVar(&config.Backend.VNI, "vni", 0, "vxlan network identifier")
	flag.UintVar(&config.Backend.Port, "port", 0, "vxlan communication port (UDP)")
	flag.Parse()
	if err := applyFlannelSubnetEnv(&config); err != nil {
		log.Fatal(err)
	}

	// wait for discoverd to come up
	status, err := cluster.WaitForHostStatus(os.Getenv("EXTERNAL_IP"), func(status *host.HostStatus) bool {
		return status.Discoverd != nil && status.Discoverd.URL != ""
	})
	if err != nil {
		log.Fatal(err)
	}

	// create service and config if not present
	client := discoverd.NewClientWithURL(status.Discoverd.URL)
	if err := client.AddService(serviceName, nil); err != nil && !hh.IsObjectExistsError(err) {
		log.Fatalf("error creating discoverd service: %s", err)
	}
	data, err := json.Marshal(map[string]Config{"config": config})
	if err != nil {
		log.Fatal(err)
	}
	// Index 0 creates metadata only when it is absent. A later overlay:set
	// must survive a flannel restart, so "already exists" is success.
	err = overlay.IgnoreExistingMeta(client.Service(serviceName).SetMeta(&discoverd.ServiceMeta{Data: data}))
	if err != nil {
		log.Fatalf("error creating discoverd service metadata: %s", err)
	}

	flanneld, err := exec.LookPath("flanneld")
	if err != nil {
		log.Fatal(err)
	}

	args := []string{
		flanneld,
		"-discoverd-url=" + status.Discoverd.URL,
		"-iface=" + os.Getenv("EXTERNAL_IP"),
		"-http-port=" + os.Getenv("PORT"),
		fmt.Sprintf("-notify-url=http://%s:1113/host/network", os.Getenv("EXTERNAL_IP")),
	}
	// If flynn-host already knows our previous subnet (persisted across
	// flynn-host restarts and reboots), ask flanneld to reuse it so the
	// bridge IP stays stable; otherwise long-running containers' cached DNS
	// resolvers keep dialing the old bridge after a subnet rotation.
	if status.Network != nil && status.Network.Subnet != "" {
		args = append(args, "-preferred-subnet="+status.Network.Subnet)
	}

	if err := syscall.Exec(flanneld, args, os.Environ()); err != nil {
		log.Fatal(err)
	}
}

// applyFlannelSubnetEnv fills unset flags from the release env. Flags win.
// Empty env leaves flannel's own default, which is 24 on a network wider than /24.
func applyFlannelSubnetEnv(config *Config) error {
	if config.SubnetLen == 0 {
		n, err := overlay.ParseSubnetLen(os.Getenv("SUBNET_LEN"))
		if err != nil {
			return err
		}
		config.SubnetLen = n
	}
	if strings.TrimSpace(config.SubnetMin) == "" {
		config.SubnetMin = strings.TrimSpace(os.Getenv("SUBNET_MIN"))
	}
	if strings.TrimSpace(config.SubnetMax) == "" {
		config.SubnetMax = strings.TrimSpace(os.Getenv("SUBNET_MAX"))
	}
	return nil
}
