package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/inconshreveable/log15"
	"github.com/opencontainers/runc/libcontainer"
	_ "github.com/opencontainers/runc/libcontainer/nsenter"
	"github.com/randy-girard/flynn/bootstrap/discovery"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	"github.com/randy-girard/flynn/host/cli"
	"github.com/randy-girard/flynn/host/config"
	"github.com/randy-girard/flynn/host/logmux"
	host "github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/host/volume"
	volumeapi "github.com/randy-girard/flynn/host/volume/api"
	volumemanager "github.com/randy-girard/flynn/host/volume/manager"
	zfsVolume "github.com/randy-girard/flynn/host/volume/zfs"
	"github.com/randy-girard/flynn/pkg/cliutil"
	"github.com/randy-girard/flynn/pkg/shutdown"
	"github.com/randy-girard/flynn/pkg/version"
)

const configFile = "/etc/flynn/host.json"

func init() {
	cli.Register("daemon", runDaemon, `
usage: flynn-host daemon [options]

options:
  --http-port=PORT           HTTP port [default: 1113]
  --external-ip=IP           external IP of host
  --listen-ip=IP             bind host network services to this IP
  --state=PATH               path to state file [default: /var/lib/flynn/host-state.bolt]
  --sink-state=PATH          path to the sink state file [default: /var/lib/flynn/sink-state.bolt]
  --id=ID                    host id
  --tags=TAGS                host tags (comma separated list of KEY=VAL pairs, used for job constraints in the scheduler)
  --force                    kill all containers booted by flynn-host before starting
  --volpath=PATH             directory to create volumes in [default: /var/lib/flynn/volumes]
  --vol-provider=VOL         volume provider [default: zfs]
  --backend=BACKEND          runner backend [default: libcontainer]
  --flynn-init=PATH          path to flynn-init binary [default: /usr/local/bin/flynn-init]
  --log-dir=DIR              directory to store job logs [default: /var/log/flynn]
  --log-file=FILE            custom log file path
  --discovery=TOKEN          join cluster with discovery token
  --discovery-service=NAME   join cluster using service discovery
  --peer-ips=IPLIST          join existing cluster using IPs
  --bridge-name=NAME         network bridge name [default: flynnbr0]
  --no-resurrect             disable cluster resurrection
  --max-job-concurrency=NUM  maximum number of jobs to start concurrently
  --partitions=PARTITIONS    specify resource partitions for host [default: system=cpu_shares:4096 background=cpu_shares:4096 user=cpu_shares:8192]
  --init-log-level=LEVEL     containerinit log level [default: info]
  --zpool-name=NAME          zpool name
  --enable-dhcp              enable DHCP server (useful to provide container IPs to VMs running in Flynn jobs)
  --auth-key=KEY             authentication key for host HTTP API (or set FLYNN_HOST_AUTH_KEY env)
	`)
}

func main() {
	// when starting a container with libcontainer, we first exec the
	// current binary with libcontainer-init as the first argument,
	// which triggers the following code to initialise the container
	// environment (namespaces, network etc.) then exec containerinit
	if len(os.Args) > 1 && os.Args[1] == "libcontainer-init" {
		runtime.GOMAXPROCS(1)
		runtime.LockOSThread()
		factory, _ := libcontainer.New("")
		if err := factory.StartInitialization(); err != nil {
			log.Fatal(err)
		}
	}

	defer shutdown.Exit()

	cli.NotifyUpgradeIfAvailable()

	usage := `usage: flynn-host [-h|--help] [--version] <command> [<args>...]

Options:
  -h, --help                 Show this message
  --version                  Show current version

Commands:
  acme                            Show ACME/Let's Encrypt status
  acme:configure                  Register a Let's Encrypt account
  acme:disable                    Disable ACME for the cluster
  acme:disable-system-routes      Disable Let's Encrypt on system app routes
  acme:enable                     Enable ACME for the cluster
  acme:enable-system-routes       Enable Let's Encrypt on system app routes
  acme:status                     Show ACME/Let's Encrypt status
  alert                           List cluster metric alerts
  alert:add                       Add a cluster metric alert
  alert:disable                   Disable a cluster metric alert
  alert:enable                    Enable a cluster metric alert
  alert:remove                    Delete a cluster metric alert
  backup                          Take a cluster backup
  bootstrap                       Bootstrap layer 1
  cli-add-command                 Get the 'flynn cluster:add' command to manage this cluster
  collect-debug-info              Collect debug information into an anonymous gist or tarball
  daemon                          Start the daemon
  demote                          Demote a Flynn node from the consensus cluster
  destroy-volumes                 Destroy the local volume database
  discover                        Return low-level information about a service
  domain                          Show cluster domain and apex (root) app
  domain:apex                     Set or clear which app serves the apex hostname
  download                        Download container images
  firewall                        Show flynn-host managed firewall rules
  firewall:expose                 Open a TCP port for an exposed service
  firewall:peer-add               Allow cluster traffic from a node IP
  firewall:peer-remove            Drop a node IP from the host firewall
  firewall:sync                   Reconcile peer IPs and exposed TCP ports
  firewall:unexpose               Close a previously exposed TCP port
  fix                             Fix a broken cluster
  github                          Show GitHub App status
  github:configure                Save the cluster GitHub App for GitHub deploys
  github:disable                  Clear the cluster GitHub App
  github:setup                    Print GitHub App permissions and setup steps
  github:status                   Show GitHub App status
  help                            Show usage for a specific command
  init                            Create cluster configuration for daemon
  inspect                         Get low-level information about a job
  list                            List ID and IP of each host
  log                             Get the logs of a job
  log-sink                        List cluster log sinks
  log-sink:add                    Add a cluster syslog sink
  log-sink:list                   List cluster or host log sinks
  log-sink:remove                 Remove a cluster log sink
  metrics                         Print a live host metrics snapshot
  migrate-domain                  Migrate the cluster base domain
  otel                            List OpenTelemetry exporters
  otel:add                        Add an OpenTelemetry exporter
  otel:remove                     Remove an OpenTelemetry exporter
  plugin:credentials              Manage GitHub credentials for plugin releases
  plugin:credentials-set          Store a GitHub token for plugin releases
  plugin:credentials-show         Show whether GitHub plugin credentials are set
  plugin:credentials-unset        Remove stored GitHub plugin credentials
  plugin:install                  Install a plugin from a path, alias, or GitHub URL
  plugin:list                     List installed plugins (--known for official plugins)
  plugin:route                    List, add, update, or remove routes for an installed plugin
  plugin:uninstall                Remove an installed plugin
  plugin:update                   Deploy a new release of an installed plugin
  plugin:update-all               Update all official plugins for this Flynn version
  promote                         Promote a Flynn node into the consensus cluster
  ps                              List jobs
  route:add                       Add an HTTP path route (cluster admin)
  run                             Run an interactive job
  runtime-profile                 List cluster runtime environments
  runtime-profile:allow-custom    Allow raw CPU/memory limits
  runtime-profile:create          Create a runtime environment
  runtime-profile:remove          Delete a custom runtime environment
  runtime-profile:update          Update a runtime environment
  signal                          Signal a job
  stop                            Stop running jobs
  tags                            List flynn-host daemon tags
  tags:del                        Delete flynn-host daemon tags
  tags:set                        Set flynn-host daemon tags
  update                          Update Flynn components
  version                         Show current version
  volume:create                   Create a data volume on a host
  volume:delete                   Delete volumes
  volume:gc                       Garbage collect unused volumes
  volume:list                     List volumes
  webhooks                        List webhook notification endpoints
  webhooks:add                    Add a webhook notification endpoint
  webhooks:remove                 Remove a webhook notification endpoint

See 'flynn-host help <command>' for more information on a specific command.
`

	if leadingVersionFlag(os.Args[1:]) {
		fmt.Println(version.String())
		return
	}

	args, _ := docopt.Parse(usage, nil, true, version.String(), true)
	cmd := args.String["<command>"]
	cmdArgs := cliutil.List(args, "<args>")

	if cmd == "help" {
		if len(cmdArgs) == 0 { // `flynn-host help`
			fmt.Println(usage)
			return
		}
		cmd = cmdArgs[0]
		cmdArgs = append(append([]string{}, cmdArgs[1:]...), "--help")
	}

	if cmd == "daemon" {
		// merge in args and env from config file, if available
		var c *config.Config
		if n := os.Getenv("FLYNN_HOST_CONFIG"); n != "" {
			var err error
			c, err = config.Open(n)
			if err != nil {
				shutdown.Fatalf("error opening config file %s: %s", n, err)
			}
		} else {
			var err error
			c, err = config.Open(configFile)
			if err != nil && !os.IsNotExist(err) {
				shutdown.Fatalf("error opening config file %s: %s", configFile, err)
			}
			if c == nil {
				c = &config.Config{}
			}
		}
		cmdArgs = append(cmdArgs, c.Args...)
		for k, v := range c.Env {
			os.Setenv(k, v)
		}
	} else if os.Getenv("FLYNN_HOST_AUTH_KEY") == "" {
		// CLI subcommands (ps, inspect, log, stop, ...) build a cluster client
		// that authenticates to the daemon with FLYNN_HOST_AUTH_KEY from the
		// environment. When the daemon has auth enabled, load the key from the
		// host config file so the CLI can authenticate without the operator
		// exporting it manually.
		if key, err := config.LoadAuthKey(configFile); err != nil {
			shutdown.Fatalf("error loading host auth key: %s", err)
		} else if key != "" {
			os.Setenv("FLYNN_HOST_AUTH_KEY", key)
		}
	}

	cmd, cmdArgs, from := cli.ResolveCommand(cmd, cmdArgs)
	if !cli.WantsHelp(cmdArgs) {
		cli.PrintCommandRename(from, cmd)
	}

	if err := cli.Run(cmd, cmdArgs); err != nil {
		if err == cli.ErrInvalidCommand {
			fmt.Printf("ERROR: %q is not a valid command\n\n", cmd)
			fmt.Println(usage)
			shutdown.ExitWithCode(1)
		} else if _, ok := err.(cli.ErrAlreadyLogged); ok {
			shutdown.ExitWithCode(1)
		}
		shutdown.Fatal(err)
	}
}

func runDaemon(args *docopt.Args) {
	hostname, _ := os.Hostname()
	httpPort := args.String["--http-port"]
	externalIP := args.String["--external-ip"]
	listenIP := args.String["--listen-ip"]
	stateFile := args.String["--state"]
	sinkFile := args.String["--sink-state"]
	hostID := args.String["--id"]
	tags := parseTagArgs(args.String["--tags"])
	force := args.Bool["--force"]
	volPath := args.String["--volpath"]
	volProvider := args.String["--vol-provider"]
	backendName := args.String["--backend"]
	flynnInit := args.String["--flynn-init"]
	logDir := args.String["--log-dir"]
	logFile := args.String["--log-file"]
	discoveryToken := args.String["--discovery"]
	discoveryService := args.String["--discovery-service"]
	bridgeName := args.String["--bridge-name"]
	enableDHCP := args.Bool["--enable-dhcp"]

	logger, err := setupLogger(logDir, logFile)
	if err != nil {
		shutdown.Fatalf("error setting up logger: %s", err)
	}

	initLogLevel, err := log15.LvlFromString(args.String["--init-log-level"])
	if err != nil {
		shutdown.Fatalf("error setting init log level: %s", err)
	}

	var peerIPs []string
	if args.String["--peer-ips"] != "" {
		peerIPs = strings.Split(args.String["--peer-ips"], ",")
	}

	if hostID == "" {
		hostID = strings.Replace(hostname, "-", "", -1)
	}

	var maxJobConcurrency uint64 = 4
	if m, err := strconv.ParseUint(args.String["--max-job-concurrency"], 10, 64); err == nil {
		maxJobConcurrency = m
	}

	zpoolName := args.String["--zpool-name"]
	if zpoolName == "" {
		zpoolName = zfsVolume.DefaultDatasetName
	}

	if path, err := filepath.Abs(flynnInit); err == nil {
		flynnInit = path
	}

	var partitionCGroups = make(map[string]int64) // name -> cpu shares
	for _, p := range strings.Split(args.String["--partitions"], " ") {
		nameShares := strings.Split(p, "=cpu_shares:")
		if len(nameShares) != 2 {
			shutdown.Fatalf("invalid partition specifier: %q", p)
		}
		shares, err := strconv.ParseInt(nameShares[1], 10, 64)
		if err != nil || shares < 2 {
			shutdown.Fatalf("invalid cpu shares specifier: %q", shares)
		}
		partitionCGroups[nameShares[0]] = shares
	}
	for _, s := range []string{"user", "system", "background"} {
		if _, ok := partitionCGroups[s]; !ok {
			shutdown.Fatalf("missing mandatory resource partition: %s", s)
		}
	}

	log := logger.New("fn", "runDaemon", "host.id", hostID)
	log.Info("starting daemon")

	log.Info("validating host ID")
	if strings.Contains(hostID, "-") {
		shutdown.Fatal("host id must not contain dashes")
	}
	if externalIP == "" {
		log.Info("detecting external IP")
		var err error
		externalIP, err = config.DefaultExternalIP()
		if err != nil {
			log.Error("error detecting external IP", "err", err)
			shutdown.Fatal(err)
		}
		log.Info("using external IP " + externalIP)
	}

	publishAddr := net.JoinHostPort(externalIP, httpPort)
	if discoveryToken != "" {
		// TODO: retry
		log.Info("registering with cluster discovery service", "token", discoveryToken, "addr", publishAddr, "name", hostID)
		discoveryID, err := discovery.RegisterInstance(discovery.Info{
			ClusterURL:  discoveryToken,
			InstanceURL: "http://" + publishAddr,
			Name:        hostID,
		})
		if err != nil {
			log.Error("error registering with cluster discovery service", "err", err)
			shutdown.Fatal(err)
		}
		log.Info("registered with cluster discovery service", "id", discoveryID)
	}

	state := NewState(hostID, stateFile)
	shutdown.BeforeExit(func() { state.CloseDB() })

	log.Info("initializing volume manager", "provider", volProvider)
	var newVolProvider func() (volume.Provider, error)
	switch volProvider {
	case "zfs":
		newVolProvider = func() (volume.Provider, error) {
			return zfsVolume.NewProvider(&zfsVolume.ProviderConfig{
				DatasetName: zpoolName,
				Make:        zfsVolume.DefaultMakeDev(volPath, log),
				WorkingDir:  filepath.Join(volPath, "zfs"),
			})
		}
	case "mock":
		newVolProvider = func() (volume.Provider, error) { return nil, nil }
	default:
		shutdown.Fatalf("unknown volume provider: %q", volProvider)
	}
	vman := volumemanager.New(
		filepath.Join(volPath, "volumes.bolt"),
		logger.New("component", "volumemanager"),
		newVolProvider,
	)
	shutdown.BeforeExit(func() { vman.CloseDB() })

	mux := logmux.New(hostID, logDir, logger.New("host.id", hostID, "component", "logmux"))
	sman := logmux.NewSinkManager(sinkFile, mux, state, logger.New("host.id", hostID, "component", "sinkManager"))
	shutdown.BeforeExit(func() { sman.CloseDB() })

	log.Info("initializing job backend", "type", backendName)
	var backend Backend
	switch backendName {
	case "libcontainer":
		backend, err = NewLibcontainerBackend(&LibcontainerConfig{
			State:            state,
			VolManager:       vman,
			BridgeName:       bridgeName,
			InitPath:         flynnInit,
			InitLogLevel:     initLogLevel,
			LogMux:           mux,
			PartitionCGroups: partitionCGroups,
			Logger:           logger.New("host.id", hostID, "component", "backend", "backend", "libcontainer"),
			EnableDHCP:       enableDHCP,
		})
	case "mock":
		backend = MockBackend{}
	default:
		shutdown.Fatalf("unknown backend %q", backendName)
	}
	if err != nil {
		shutdown.Fatal(err)
	}
	backend.SetDefaultEnv("EXTERNAL_IP", externalIP)
	backend.SetDefaultEnv("LISTEN_IP", listenIP)

	var buffers host.LogBuffers
	// Read auth key from flag or environment
	authKey := args.String["--auth-key"]
	if authKey == "" {
		authKey = os.Getenv("FLYNN_HOST_AUTH_KEY")
	}
	if authKey != "" {
		log.Info("host HTTP API authentication enabled")
	} else {
		log.Warn("host HTTP API authentication disabled (set --auth-key or FLYNN_HOST_AUTH_KEY)")
	}

	discoverdManager := NewDiscoverdManager(backend, sman, hostID, publishAddr, tags)
	publishURL := "http://" + publishAddr
	webhookDisp := NewWebhookDispatcher(hostID, state, logger)
	go webhookDisp.Run()
	shutdown.BeforeExit(func() {
		webhookDisp.Send("D11", "Daemon shutting down", "info", "", nil, nil)
		webhookDisp.Shutdown()
	})
	state.webhookDispatcher = webhookDisp
	state.logMux = mux

	host := &Host{
		id:  hostID,
		url: publishURL,
		status: &host.HostStatus{
			ID:   hostID,
			URL:  publishURL,
			Tags: tags,
		},
		state:             state,
		backend:           backend,
		vman:              vman,
		sman:              sman,
		volAPI:            volumeapi.NewHTTPAPI(vman),
		discMan:           discoverdManager,
		log:               logger.New("host.id", hostID),
		authKey:           authKey,
		webhookDispatcher: webhookDisp,
		maxJobConcurrency: maxJobConcurrency,
	}
	backend.SetHost(host)

	// restore the host status if set in the environment
	if statusEnv := os.Getenv("FLYNN_HOST_STATUS"); statusEnv != "" {
		log.Info("restoring host status from parent")
		if err := json.Unmarshal([]byte(statusEnv), &host.status); err != nil {
			log.Error("error restoring host status from parent", "err", err)
			shutdown.Fatal(err)
		}
		// keep the same tags as the parent
		discoverdManager.UpdateTags(host.status.Tags)
	}
	pid := os.Getpid()
	log.Info("setting host status PID", "pid", pid)
	host.status.PID = pid
	host.status.Version = version.String()
	host.status.Auth = authKey != ""
	if len(os.Args) > 2 {
		host.status.Flags = os.Args[2:]
	}

	log.Info("creating HTTP listener")
	l, err := newHTTPListener(net.JoinHostPort(listenIP, httpPort))
	if err != nil {
		log.Error("error creating HTTP listener", "err", err)
		shutdown.Fatal(err)
	}
	host.listener = l
	shutdown.BeforeExit(func() { host.Close() })

	// if we have a control socket FD, wait for a "resume" message before
	// opening state DBs and serving requests.
	var controlFD int
	if fdEnv := os.Getenv("FLYNN_CONTROL_FD"); fdEnv != "" {
		log.Info("parsing control socket file descriptor")
		controlFD, err = strconv.Atoi(fdEnv)
		if err != nil {
			log.Error("error parsing control socket file descriptor", "err", err)
			shutdown.Fatal(err)
		}

		log.Info("waiting for resume message from parent")
		msg := make([]byte, len(ControlMsgResume))
		if _, err := syscall.Read(controlFD, msg); err != nil {
			log.Error("error waiting for resume message from parent", "err", err)
			shutdown.Fatal(err)
		}

		log.Info("validating resume message")
		if !bytes.Equal(msg, ControlMsgResume) {
			log.Error(fmt.Sprintf("unexpected resume message from parent: %v", msg))
			shutdown.ExitWithCode(1)
		}

		log.Info("receiving log buffers from parent")
		if err := json.NewDecoder(&controlSock{controlFD}).Decode(&buffers); err != nil {
			log.Error("error receiving log buffers from parent", "err", err)
			shutdown.Fatal(err)
		}
	}

	log.Info("opening state databases")
	if err := host.OpenDBs(); err != nil {
		log.Error("error opening state databases", "err", err)
		shutdown.Fatal(err)
	}

	// stopJobs stops all jobs, leaving discoverd until the end so other
	// jobs can unregister themselves on shutdown.
	stopJobs := func() (err error) {
		var except []string
		host.statusMtx.RLock()
		if host.status.Discoverd != nil && host.status.Discoverd.JobID != "" {
			except = []string{host.status.Discoverd.JobID}
		}
		host.statusMtx.RUnlock()
		log.Info("stopping all jobs except discoverd")
		if err := backend.Cleanup(except); err != nil {
			log.Error("error stopping all jobs except discoverd", "err", err)
			return err
		}
		for _, id := range except {
			log.Info("stopping discoverd")
			if e := backend.Stop(id); e != nil {
				log.Error("error stopping discoverd", "err", err)
				err = e
			}
		}
		return
	}

	log.Info("restoring state")
	resurrect, err := state.Restore(backend, buffers)
	if err != nil {
		log.Error("error restoring state", "err", err)
		shutdown.Fatal(err)
	}
	// Intentionally do NOT stop jobs or unregister from discoverd on exit.
	// Job containers are independent processes (no Pdeathsig) and the systemd
	// unit uses KillMode=process, so they survive the daemon exiting. Leaving
	// them running makes `systemctl restart flynn-host` (used by the updater)
	// non-destructive: the freshly started daemon's state.Restore reconnects
	// to the still-running containers instead of resurrecting them, and the
	// host stays registered in discoverd so the scheduler doesn't churn. The
	// previous BeforeExit handler called discoverdManager.Close()+stopJobs(),
	// which tore down every container on each restart and forced a full
	// resurrection (postgres re-clone, sirenia re-election, etc.).

	log.Info("serving HTTP requests")
	host.ServeHTTP()
	webhookDisp.Send("D10", "Daemon started", "info", "", nil, nil)
	host.startDiskWatch()
	host.startContainerMetricsLogs()

	if controlFD > 0 {
		// now that we are serving requests, send an "ok" message to the parent
		log.Info("sending ok message to parent")
		if _, err := syscall.Write(controlFD, ControlMsgOK); err != nil {
			log.Error("error sending ok message to parent", "err", err)
			shutdown.Fatal(err)
		}

		log.Info("closing control socket")
		if err := syscall.Close(controlFD); err != nil {
			log.Error("error closing control socket", "err", err)
		}
	}

	if force {
		log.Info("forcibly stopping existing jobs")
		if err := stopJobs(); err != nil {
			log.Error("error forcibly stopping existing jobs", "err", err)
			shutdown.Fatal(err)
		}
	}

	if discoveryToken != "" {
		log.Info("getting cluster peer IPs")
		instances, err := discovery.GetCluster(discoveryToken)
		if err != nil {
			// TODO(titanous): retry?
			log.Error("error getting discovery cluster", "err", err)
			shutdown.Fatal(err)
		}
		peerIPs = make([]string, 0, len(instances))
		for _, inst := range instances {
			u, err := url.Parse(inst.URL)
			if err != nil {
				continue
			}
			ip, _, err := net.SplitHostPort(u.Host)
			if err != nil || ip == externalIP {
				continue
			}
			peerIPs = append(peerIPs, ip)
		}
		log.Info("got cluster peer IPs", "peers", peerIPs)
	} else if discoveryService != "" {
		log.Info("registering with service discovery", "service", discoveryService)
		hb, err := discoverd.Register(discoveryService, net.JoinHostPort(listenIP, httpPort))
		if err != nil {
			log.Error("error registering with service discovery", "err", err)
			shutdown.Fatal(err)
		}
		shutdown.BeforeExit(func() { hb.Close() })

		log.Info("determining cluster size", "service", discoveryService)
		meta, err := discoverd.NewService(discoveryService).GetMeta()
		if err != nil {
			log.Error("error getting discovery service metadata", "err", err)
			shutdown.Fatal(err)
		}
		var cluster struct{ Size int }
		if err := json.Unmarshal(meta.Data, &cluster); err != nil {
			log.Error("error parsing discovery service metadata", "err", err)
			shutdown.Fatal(err)
		}

		if cluster.Size > 1 {
			log.Info("getting cluster peers from service discovery", "service", discoveryService)
			instances, err := discoverd.GetInstances(discoveryService, 30*time.Second)
			if err != nil {
				log.Error("error getting cluster peers from service discovery", "err", err)
				shutdown.Fatal(err)
			}
			if len(instances) >= cluster.Size {
				peerIPs = make([]string, 0, len(instances))
				for _, inst := range instances {
					if ip := inst.Host(); ip != externalIP {
						peerIPs = append(peerIPs, ip)
					}
				}
			}
		}
	}
	log.Info("connecting to cluster peers", "ips", peerIPs)
	startHostFirewall(externalIP, peerIPs, log)
	if err := discoverdManager.ConnectPeer(peerIPs); err != nil {
		log.Info("no cluster peers available")
	}

	if !args.Bool["--no-resurrect"] {
		log.Info("resurrecting jobs")
		resurrect()
	}

	monitor := NewMonitor(host.discMan, externalIP, logger)
	shutdown.BeforeExit(func() { monitor.Shutdown() })
	go monitor.Run()

	log.Info("blocking main goroutine")
	<-make(chan struct{})
}

func leadingVersionFlag(argv []string) bool {
	for _, a := range argv {
		if a == "--version" {
			return true
		}
		if a == "-h" || a == "--help" {
			continue
		}
		return false
	}
	return false
}

func parseTagArgs(args string) map[string]string {
	tags := make(map[string]string)
	for _, s := range strings.Split(args, ",") {
		keyVal := strings.SplitN(s, "=", 2)
		if len(keyVal) == 1 && keyVal[0] != "" {
			tags[keyVal[0]] = "true"
		} else if len(keyVal) == 2 {
			tags[keyVal[0]] = keyVal[1]
		}
	}
	return tags
}

func setupLogger(logDir, logFile string) (log15.Logger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, err
	}
	if logFile == "" {
		logFile = filepath.Join(logDir, "flynn-host.log")
	}
	handler, err := log15.FileHandler(logFile, log15.LogfmtFormat())
	if err != nil {
		return nil, err
	}
	log15.Root().SetHandler(handler)
	return log15.New("app", "host", "pid", os.Getpid()), nil
}
