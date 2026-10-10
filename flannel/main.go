package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	log "github.com/golang/glog"
	disc "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/flannel/backend"
	"github.com/randy-girard/flynn/flannel/backend/alloc"
	"github.com/randy-girard/flynn/flannel/backend/hostgw"
	"github.com/randy-girard/flynn/flannel/backend/vxlan"
	"github.com/randy-girard/flynn/flannel/discoverd"
	"github.com/randy-girard/flynn/flannel/pkg/ip"
	"github.com/randy-girard/flynn/flannel/pkg/task"
	"github.com/randy-girard/flynn/flannel/subnet"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/keepalive"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/pkg/version"
)

type CmdLineOpts struct {
	help            bool
	version         bool
	ipMasq          bool
	subnetFile      string
	iface           string
	notifyURL       string
	discoverdURL    string
	httpPort        string
	preferredSubnet string
}

var opts CmdLineOpts

func init() {
	flag.StringVar(&opts.subnetFile, "subnet-file", "/run/flannel/subnet.env", "filename where env variables (subnet and MTU values) will be written to")
	flag.StringVar(&opts.notifyURL, "notify-url", "", "URL to send webhook after starting")
	flag.StringVar(&opts.discoverdURL, "discoverd-url", "", "URL of discoverd registry")
	flag.StringVar(&opts.iface, "iface", "", "interface to use (IP or name) for inter-host communication")
	flag.StringVar(&opts.httpPort, "http-port", "5001", "port to listen for HTTP requests on allocated IP")
	flag.BoolVar(&opts.ipMasq, "ip-masq", false, "setup IP masquerade rule for traffic destined outside of overlay network")
	flag.BoolVar(&opts.help, "help", false, "print this message")
	flag.BoolVar(&opts.version, "version", false, "print version and exit")
	flag.StringVar(&opts.preferredSubnet, "preferred-subnet", "", "preferred subnet (CIDR) to reuse if available; keeps bridge IP stable across reboots")
}

// flagsFromEnv parses all registered flags in the given flagset,
// and if they are not already set it attempts to set their values from
// environment variables. Environment variables take the name of the flag but
// are UPPERCASE, have the given prefix, and any dashes are replaced by
// underscores - for example: some-flag => PREFIX_SOME_FLAG
func flagsFromEnv(prefix string, fs *flag.FlagSet) {
	alreadySet := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		alreadySet[f.Name] = true
	})
	fs.VisitAll(func(f *flag.Flag) {
		if !alreadySet[f.Name] {
			key := strings.ToUpper(prefix + "_" + strings.Replace(f.Name, "-", "_", -1))
			val := os.Getenv(key)
			if val != "" {
				fs.Set(f.Name, val)
			}
		}
	})
}

func writeSubnetFile(sn *backend.SubnetDef) error {
	net := sn.Net
	net.IP = net.FirstUsable()

	dir, name := filepath.Split(opts.subnetFile)
	os.MkdirAll(dir, 0755)

	tempFile := filepath.Join(dir, "."+name)
	f, err := os.Create(tempFile)
	if err != nil {
		return err
	}

	fmt.Fprintf(f, "FLANNEL_SUBNET=%s\n", net)
	fmt.Fprintf(f, "FLANNEL_MTU=%d\n", sn.MTU)
	_, err = fmt.Fprintf(f, "FLANNEL_IPMASQ=%v\n", opts.ipMasq)
	f.Close()
	if err != nil {
		return err
	}

	// rename(2) the temporary file to the desired location so that it becomes
	// atomically visible with the contents
	return os.Rename(tempFile, opts.subnetFile)
}

func notifyWebhook(sn *backend.SubnetDef) error {
	if opts.notifyURL == "" {
		return nil
	}
	net := sn.Net
	net.IP = net.FirstUsable()
	data := struct {
		JobID  string `json:"job_id"`
		Subnet string `json:"subnet"`
		MTU    int    `json:"mtu"`
	}{os.Getenv("FLYNN_JOB_ID"), net.String(), sn.MTU}
	payload, _ := json.Marshal(data)
	res, err := httpclient.PostWithHostAuth(opts.notifyURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func lookupIface() (*net.Interface, net.IP, error) {
	var iface *net.Interface
	var ipaddr net.IP
	var err error

	if len(opts.iface) > 0 {
		if ipaddr = net.ParseIP(opts.iface); ipaddr != nil {
			iface, err = ip.GetInterfaceByIP(ipaddr)
			if err != nil {
				return nil, nil, fmt.Errorf("Error looking up interface %s: %s", opts.iface, err)
			}
		} else {
			iface, err = net.InterfaceByName(opts.iface)
			if err != nil {
				return nil, nil, fmt.Errorf("Error looking up interface %s: %s", opts.iface, err)
			}
		}
	} else {
		log.Info("Determining IP address of default interface")
		if iface, err = ip.GetDefaultGatewayIface(); err != nil {
			return nil, nil, fmt.Errorf("Failed to get default interface: %s", err)
		}
	}

	if ipaddr == nil {
		ipaddr, err = ip.GetIfaceIP4Addr(iface)
		if err != nil {
			return nil, nil, fmt.Errorf("Failed to find IPv4 address for interface %s", iface.Name)
		}
	}

	return iface, ipaddr, nil
}

func makeSubnetManager() *subnet.SubnetManager {
	var registryFn func() (subnet.Registry, error)
	client := disc.NewClientWithURL(opts.discoverdURL)
	registryFn = func() (subnet.Registry, error) {
		return discoverd.NewRegistry(client, "flannel")
	}

	for {
		reg, err := registryFn()
		if err != nil {
			log.Error("Failed to create subnet registry: ", err)
			time.Sleep(time.Second)
			continue
		}

		sm, err := subnet.NewSubnetManager(reg)
		if err == nil {
			return sm
		}

		log.Error("Failed to create SubnetManager: ", err)
		time.Sleep(time.Second)
	}
}

func newBackend() (backend.Backend, *subnet.SubnetManager, error) {
	sm := makeSubnetManager()
	config := sm.GetConfig()

	var bt struct {
		Type string
	}

	if len(config.Backend) == 0 {
		bt.Type = "vxlan"
	} else {
		if err := json.Unmarshal(config.Backend, &bt); err != nil {
			return nil, nil, fmt.Errorf("Error decoding Backend property of config: %v", err)
		}
	}

	switch strings.ToLower(bt.Type) {
	case "alloc":
		return alloc.New(sm), sm, nil
	case "host-gw":
		return hostgw.New(sm), sm, nil
	case "vxlan":
		return vxlan.New(sm, config.Backend), sm, nil
	default:
		return nil, nil, fmt.Errorf("'%v': unknown backend type", bt.Type)
	}
}

func httpServer(sn *subnet.SubnetManager, publicIP, port string) error {
	http.HandleFunc("/ping", func(http.ResponseWriter, *http.Request) {})
	status.AddHandler(status.SimpleHandler(func() error {
		return pingLeases(sn.Leases(), sn.Lease())
	}))

	// Public IP first: this is always assigned (EXTERNAL_IP / lo:0). Overlay
	// bind is the bridge address, which notifyWebhook creates for the alloc
	// backend. The lease network address (e.g. 100.100.9.0) is never on an
	// interface, so listening there fails with EADDRNOTAVAIL.
	publicListener, err := net.Listen("tcp", net.JoinHostPort(publicIP, port))
	if err != nil {
		return err
	}
	go http.Serve(keepalive.Listener(publicListener), nil)

	overlayAddr := net.JoinHostPort(sn.Lease().Network.FirstUsable().String(), port)
	overlayListener, err := net.Listen("tcp", overlayAddr)
	if err != nil {
		log.Warningf("overlay HTTP listen %s failed: %v", overlayAddr, err)
		return nil
	}
	go http.Serve(keepalive.Listener(overlayListener), nil)
	return nil
}

func leaseHTTPPort(l subnet.SubnetLease) string {
	if l.Attrs.HTTPPort != "" {
		return l.Attrs.HTTPPort
	}
	return "5001"
}

// pingLeaseURLs is the status probe order for a flannel lease. PublicIP is
// first: httpServer always binds EXTERNAL_IP, while overlay FirstUsable (the
// bridge .1) is not assigned on flannel.1 (we put a /32 of the network
// address there). Pinging only FirstUsable marked every multi-node cluster
// unhealthy and blocked flynn-host update / rolling reload for 10 minutes.
func pingLeaseURLs(l subnet.SubnetLease) []string {
	port := leaseHTTPPort(l)
	var urls []string
	if l.Attrs.PublicIP != 0 {
		urls = append(urls, fmt.Sprintf("http://%s:%s/ping", l.Attrs.PublicIP, port))
	}
	if first := l.Network.FirstUsable(); first != 0 {
		urls = append(urls, fmt.Sprintf("http://%s:%s/ping", first, port))
	}
	return urls
}

func pingOneLease(client *http.Client, l subnet.SubnetLease) bool {
	for _, u := range pingLeaseURLs(l) {
		res, err := client.Get(u)
		if err != nil {
			continue
		}
		code := res.StatusCode
		res.Body.Close()
		if code == 200 {
			return true
		}
	}
	return false
}

// pingLeases reports flannel healthy when this host's public /ping answers, or
// any peer does. Overlay-only pings are a fallback; they must not be required
// for cluster update/reload, or a missing L3 neigh after crash-reboot leaves
// the cluster stuck ReadOnly-healthy except flannel.
func pingLeases(leases []subnet.SubnetLease, self subnet.SubnetLease) error {
	const timeout = 1 * time.Second
	if len(leases) <= 1 {
		return nil
	}
	client := &http.Client{Timeout: timeout}
	if pingOneLease(client, self) {
		return nil
	}
	for _, l := range leases {
		if l.Network.Equal(self.Network) {
			continue
		}
		if pingOneLease(client, l) {
			return nil
		}
	}
	return errors.New("failed to successfully ping a neighbor")
}

func run(be backend.Backend, sm *subnet.SubnetManager, exit chan int) {
	var err error
	defer func() {
		if err == nil || err == task.ErrCanceled {
			exit <- 0
		} else {
			log.Error(err)
			exit <- 1
		}
	}()

	iface, ipaddr, err := lookupIface()
	if err != nil {
		return
	}

	if iface.MTU == 0 {
		err = fmt.Errorf("Failed to determine MTU for %s interface", ipaddr)
		return
	}

	log.Infof("Using %s as external interface", ipaddr)

	// Register the flannel instance before acquiring a subnet lease so
	// pruneExpiredSubnets will treat this host as live and will not reclaim
	// its /24. Closing the heartbeater on exit lets the lease expire with
	// the instance.
	var hb disc.Heartbeater
	if opts.discoverdURL != "" {
		client := disc.NewClientWithURL(opts.discoverdURL)
		addr := net.JoinHostPort(ipaddr.String(), opts.httpPort)
		for {
			hb, err = client.AddServiceAndRegister("flannel", addr)
			if err == nil {
				break
			}
			log.Error("Failed to register flannel instance: ", err)
			time.Sleep(time.Second)
		}
		defer hb.Close()
	}

	sn, err := be.Init(iface, ipaddr, opts.httpPort, opts.ipMasq)
	if err != nil {
		return
	}

	writeSubnetFile(sn)
	notifyWebhook(sn)

	if err = httpServer(sm, ipaddr.String(), opts.httpPort); err != nil {
		err = fmt.Errorf("error starting HTTP server: %s", err)
		return
	}

	log.Infof("%s mode initialized", be.Name())
	be.Run()
}

func main() {
	// glog will log to tmp files by default. override so all entries
	// can flow into journald (if running under systemd)
	flag.Set("logtostderr", "true")

	// now parse command line args
	flag.Parse()

	if opts.help {
		fmt.Fprintf(os.Stderr, "Usage: %s [OPTION]...\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(0)
	}

	if opts.version {
		fmt.Fprintln(os.Stderr, version.String())
		os.Exit(0)
	}

	flagsFromEnv("FLANNELD", flag.CommandLine)

	be, sm, err := newBackend()
	if err != nil {
		log.Info(err)
		os.Exit(1)
	}

	// If a preferred subnet is configured (typically supplied by
	// flannel-wrapper from the host's persisted NetworkConfig), ask the
	// subnet manager to prefer it when acquiring a lease so the bridge IP
	// stays stable across reboots.
	if opts.preferredSubnet != "" {
		if err := sm.SetPreferredSubnet(opts.preferredSubnet); err != nil {
			log.Errorf("invalid -preferred-subnet %q: %v (ignoring)", opts.preferredSubnet, err)
		}
	}

	// Register for SIGINT and SIGTERM and wait for one of them to arrive
	log.Info("Installing signal handlers")
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	exit := make(chan int)
	go run(be, sm, exit)

	for {
		select {
		case <-sigs:
			// unregister to get default OS nuke behaviour in case we don't exit cleanly
			signal.Stop(sigs)

			log.Info("Exiting...")
			be.Stop()

		case code := <-exit:
			log.Infof("%s mode exited", be.Name())
			os.Exit(code)
		}
	}
}
