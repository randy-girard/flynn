package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/flynn/flynn/appliance/kafka"
	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/random"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/inconshreveable/log15"
)

const (
	// nodeIDKey is the discoverd metadata key advertising a broker's KRaft node id.
	nodeIDKey = "KAFKA_NODE_ID"

	// bootstrapIDKey is the discoverd metadata key advertising a broker's stable
	// bootstrap identity used to assign node ids on first cluster formation.
	bootstrapIDKey = "KAFKA_BOOTSTRAP_ID"

	// controllerAddrKey is the discoverd metadata key advertising a broker's
	// controller quorum endpoint.
	controllerAddrKey = "KAFKA_CONTROLLER_ADDR"

	// DefaultHTTPPort is the admin API port.
	DefaultHTTPPort = "9095"
)

func main() {
	// When invoked as `flynn-kafka admin <tool> [args...]` this process becomes
	// a thin, TLS-aware wrapper around the kafka-*.sh admin tools. This is what
	// the `flynn kafka` CLI runs as a one-off job so it never has to know about
	// keystores or bootstrap addresses.
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		runAdmin(os.Args[2:])
		return
	}
	runBroker()
}

func runBroker() {
	logger := log15.New("app", "kafka")

	serviceName := os.Getenv("FLYNN_KAFKA")
	if serviceName == "" {
		serviceName = "kafka"
	}
	singleton := os.Getenv("SINGLETON") == "true"
	ip := os.Getenv("EXTERNAL_IP")
	if ip == "" {
		shutdown.Fatal("EXTERNAL_IP is required")
	}
	clusterID := os.Getenv("KAFKA_CLUSTER_ID")
	if clusterID == "" {
		shutdown.Fatal("KAFKA_CLUSTER_ID is required")
	}
	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = DefaultHTTPPort
	}

	brokerCount := 1
	if !singleton {
		brokerCount = envInt("KAFKA_BROKER_COUNT", 3)
	}
	replication := envInt("KAFKA_REPLICATION_FACTOR", min(brokerCount, 3))
	minISR := envInt("KAFKA_MIN_ISR", max(replication-1, 1))
	numPartitions := envInt("KAFKA_NUM_PARTITIONS", 3)

	bootstrapID, err := kafka.LoadOrCreateBootstrapID(kafka.DefaultDataDir)
	if err != nil {
		shutdown.Fatal(err)
	}
	controllerAddr := fmt.Sprintf("%s:%s", ip, kafka.DefaultControllerPort)

	logger.Info("registering with discoverd", "service", serviceName, "bootstrap.id", bootstrapID)
	if err := discoverd.DefaultClient.AddService(serviceName, nil); err != nil && !httphelper.IsObjectExistsError(err) {
		shutdown.Fatal(err)
	}
	inst := &discoverd.Instance{
		Addr: fmt.Sprintf("%s:%s", ip, kafka.DefaultPort),
		Meta: map[string]string{
			bootstrapIDKey:    bootstrapID,
			controllerAddrKey: controllerAddr,
		},
	}
	hb, err := discoverd.DefaultClient.RegisterInstance(serviceName, inst)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { hb.Close() })

	logger.Info("waiting for bootstrap peers", "expected", brokerCount)
	bootstrapIDs, err := waitForBootstrapIDs(serviceName, brokerCount, 10*time.Minute)
	if err != nil {
		shutdown.Fatal(err)
	}

	nodeID, err := kafka.ResolveNodeID(kafka.DefaultDataDir, bootstrapID, bootstrapIDs)
	if err != nil {
		shutdown.Fatal(err)
	}
	if err := hb.SetMeta(map[string]string{
		bootstrapIDKey:    bootstrapID,
		nodeIDKey:         strconv.Itoa(nodeID),
		controllerAddrKey: controllerAddr,
	}); err != nil {
		shutdown.Fatal(err)
	}
	logger.Info("resolved node id", "node.id", nodeID, "bootstrap.id", bootstrapID)

	// Wait for the whole quorum to register so every broker computes an
	// identical controller.quorum.voters value.
	logger.Info("waiting for peers", "expected", brokerCount)
	voters, err := waitForVoters(serviceName, brokerCount, 10*time.Minute)
	if err != nil {
		shutdown.Fatal(err)
	}
	quorumVoters := kafka.BuildQuorumVoters(voters)
	logger.Info("quorum discovered", "voters", quorumVoters)

	process := kafka.NewProcess()
	process.NodeID = nodeID
	process.ClusterID = clusterID
	process.QuorumVoters = quorumVoters
	process.AdvertisedHost = ip
	process.Singleton = singleton
	process.ReplicationFactor = replication
	process.MinInSyncReplicas = minISR
	process.NumPartitions = numPartitions
	process.Logger = logger.New("component", "process", "node.id", nodeID)

	// Enable mutual TLS on the CLIENT listener when certificates are supplied.
	if os.Getenv("KAFKA_TLS_ENABLED") == "true" {
		if err := configureTLS(process, logger); err != nil {
			shutdown.Fatal(err)
		}
	}

	logger.Info("starting broker", "tls", process.TLSEnabled)
	if err := process.Start(); err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { process.Stop() })

	handler := kafka.NewHandler()
	handler.Process = process
	handler.Heartbeater = hb
	handler.Logger = logger.New("component", "http")

	logger.Info("serving admin api", "port", httpPort)
	shutdown.Fatal(http.ListenAndServe(":"+httpPort, handler))
}

// configureTLS materializes the broker keystore/truststore for the CLIENT
// listener and a local client.properties used by admin operations.
func configureTLS(process *kafka.Process, logger log15.Logger) error {
	ca := os.Getenv("KAFKA_TRUSTED_CERT")
	serverCert := os.Getenv("KAFKA_TLS_SERVER_CERT")
	serverKey := os.Getenv("KAFKA_TLS_SERVER_KEY")
	clientCert := os.Getenv("KAFKA_CLIENT_CERT")
	clientKey := os.Getenv("KAFKA_CLIENT_CERT_KEY")
	if ca == "" || serverCert == "" || serverKey == "" {
		return fmt.Errorf("KAFKA_TLS_ENABLED is set but server certificate material is missing")
	}

	dir := filepath.Join(process.DataDir, "tls")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	password := random.String(24)

	logger.Info("building broker keystore/truststore")
	keystore, truststore, err := kafka.WriteServerStores(dir, ca, serverCert, serverKey, password)
	if err != nil {
		return err
	}

	process.TLSEnabled = true
	process.KeystorePath = keystore
	process.TruststorePath = truststore
	process.KeystorePassword = password
	process.TruststorePassword = password

	// Local admin operations (health checks, managed API) also speak SSL.
	if clientCert != "" && clientKey != "" {
		propsPath, err := kafka.WriteClientConfig(dir, ca, clientCert, clientKey, password)
		if err != nil {
			return err
		}
		process.CommandConfigPath = propsPath
	}
	return nil
}

// runAdmin execs a kafka-*.sh tool, injecting the bootstrap server and, when
// TLS is enabled, an SSL --command-config generated from the environment.
func runAdmin(args []string) {
	if len(args) == 0 {
		shutdown.Fatal("usage: flynn-kafka admin <tool> [args...]")
	}
	tool := filepath.Join(kafka.DefaultBinDir, args[0])
	rest := args[1:]

	bootstrap := os.Getenv("KAFKA_BOOTSTRAP_SERVERS")
	if bootstrap == "" {
		shutdown.Fatal("KAFKA_BOOTSTRAP_SERVERS is required")
	}

	final := append([]string{tool, "--bootstrap-server", bootstrap}, rest...)

	if os.Getenv("KAFKA_TRUSTED_CERT") != "" {
		dir, err := os.MkdirTemp("", "kafka-admin")
		if err != nil {
			shutdown.Fatal(err)
		}
		propsPath, err := kafka.WriteClientConfig(dir,
			os.Getenv("KAFKA_TRUSTED_CERT"),
			os.Getenv("KAFKA_CLIENT_CERT"),
			os.Getenv("KAFKA_CLIENT_CERT_KEY"),
			random.String(24),
		)
		if err != nil {
			shutdown.Fatal(err)
		}
		final = append(final, "--command-config", propsPath)
	}

	if _, err := exec.LookPath(tool); err != nil {
		shutdown.Fatalf("unknown kafka tool %q: %s", args[0], err)
	}
	if err := syscall.Exec(tool, final, os.Environ()); err != nil {
		shutdown.Fatal(err)
	}
}

// waitForBootstrapIDs polls discoverd until at least count brokers have
// registered a bootstrap id.
func waitForBootstrapIDs(service string, count int, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		insts, err := discoverd.GetInstances(service, 30*time.Second)
		if err != nil && time.Now().After(deadline) {
			return nil, err
		}

		ids := uniqueBootstrapIDs(insts)
		if len(ids) >= count {
			return ids, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %d bootstrap peers, found %d", count, len(ids))
		}
		time.Sleep(time.Second)
	}
}

// waitForVoters polls discoverd until at least count brokers have registered,
// returning a map of node id -> controller address. When multiple instances
// advertise the same node id, the newest registration wins.
func waitForVoters(service string, count int, timeout time.Duration) (map[int]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		insts, err := discoverd.GetInstances(service, 30*time.Second)
		if err != nil && time.Now().After(deadline) {
			return nil, err
		}

		voters := votersFromInstances(insts)
		if len(voters) >= count {
			return voters, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %d brokers, found %d", count, len(voters))
		}
		time.Sleep(time.Second)
	}
}

func uniqueBootstrapIDs(insts []*discoverd.Instance) []string {
	seen := make(map[string]struct{})
	for _, inst := range insts {
		if id := inst.Meta[bootstrapIDKey]; id != "" {
			seen[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

func votersFromInstances(insts []*discoverd.Instance) map[int]string {
	best := make(map[int]*discoverd.Instance)
	for _, inst := range insts {
		idStr := inst.Meta[nodeIDKey]
		addr := inst.Meta[controllerAddrKey]
		if idStr == "" || addr == "" {
			continue
		}
		id, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		if cur, ok := best[id]; !ok || inst.Index > cur.Index {
			best[id] = inst
		}
	}

	voters := make(map[int]string, len(best))
	for id, inst := range best {
		voters[id] = inst.Meta[controllerAddrKey]
	}
	return voters
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
