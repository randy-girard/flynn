package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/flynn/flynn/appliance/clickhouse"
	"github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/httphelper"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/inconshreveable/log15"
)

const (
	keeperServerIDKey = "CLICKHOUSE_KEEPER_SERVER_ID"
	keeperRaftAddrKey = "CLICKHOUSE_KEEPER_RAFT_ADDR"

	replicaIDKey      = "CLICKHOUSE_REPLICA_ID"
	replicaNativeKey  = "CLICKHOUSE_NATIVE_ADDR"
	replicaIntersrvKey = "CLICKHOUSE_INTERSERVER_ADDR"

	defaultHTTPPort = "9090"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		runAdmin(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "keeper" {
		os.Args = os.Args[1:]
		runKeeper()
		return
	}
	runServer()
}

func runKeeper() {
	logger := log15.New("app", "clickhouse-keeper")

	serviceName := keeperServiceName()
	ip := os.Getenv("EXTERNAL_IP")
	if ip == "" {
		shutdown.Fatal("EXTERNAL_IP is required")
	}
	replicaCount := envInt("CLICKHOUSE_REPLICA_COUNT", 1)

	serverID := ipToNodeID(net.ParseIP(ip))
	raftAddr := fmt.Sprintf("%s:%s", ip, clickhouse.DefaultKeeperRaftPort)

	logger.Info("registering keeper with discoverd", "service", serviceName, "server.id", serverID)
	if err := discoverd.DefaultClient.AddService(serviceName, nil); err != nil && !httphelper.IsObjectExistsError(err) {
		shutdown.Fatal(err)
	}
	inst := &discoverd.Instance{
		Addr: fmt.Sprintf("%s:%s", ip, clickhouse.DefaultKeeperClientPort),
		Meta: map[string]string{
			keeperServerIDKey: strconv.Itoa(serverID),
			keeperRaftAddrKey: raftAddr,
		},
	}
	hb, err := discoverd.DefaultClient.RegisterInstance(serviceName, inst)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { hb.Close() })

	logger.Info("waiting for keeper peers", "expected", replicaCount)
	keepers, err := waitForKeepers(serviceName, replicaCount, 10*time.Minute)
	if err != nil {
		shutdown.Fatal(err)
	}

	process := clickhouse.NewKeeperProcess()
	process.ServerID = serverID
	process.Servers = clickhouse.BuildKeeperServers(keepers, clickhouse.DefaultKeeperRaftPort)
	process.DataDir = clickhouse.DefaultDataDir
	process.Logger = logger.New("component", "process", "server.id", serverID)

	logger.Info("starting keeper")
	if err := process.Start(); err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { process.Stop() })
	<-(chan struct{})(nil)
}

func runServer() {
	logger := log15.New("app", "clickhouse")

	serviceName := os.Getenv("FLYNN_CLICKHOUSE")
	if serviceName == "" {
		serviceName = "clickhouse"
	}
	ip := os.Getenv("EXTERNAL_IP")
	if ip == "" {
		shutdown.Fatal("EXTERNAL_IP is required")
	}
	replicaCount := envInt("CLICKHOUSE_REPLICA_COUNT", 1)
	httpPort := os.Getenv("HTTP_PORT")
	if httpPort == "" {
		httpPort = defaultHTTPPort
	}

	replicaID := ipToNodeID(net.ParseIP(ip))
	nativeAddr := fmt.Sprintf("%s:%s", ip, clickhouse.DefaultNativePort)
	intersrvAddr := fmt.Sprintf("%s:%s", ip, clickhouse.DefaultInterserverPort)

	logger.Info("registering with discoverd", "service", serviceName, "replica.id", replicaID)
	if err := discoverd.DefaultClient.AddService(serviceName, nil); err != nil && !httphelper.IsObjectExistsError(err) {
		shutdown.Fatal(err)
	}
	inst := &discoverd.Instance{
		Addr: nativeAddr,
		Meta: map[string]string{
			replicaIDKey:       strconv.Itoa(replicaID),
			replicaNativeKey:   nativeAddr,
			replicaIntersrvKey: intersrvAddr,
		},
	}
	hb, err := discoverd.DefaultClient.RegisterInstance(serviceName, inst)
	if err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { hb.Close() })

	keeperService := keeperServiceName()
	logger.Info("waiting for keeper quorum", "expected", replicaCount)
	keepers, err := waitForKeepers(keeperService, replicaCount, 10*time.Minute)
	if err != nil {
		shutdown.Fatal(err)
	}

	logger.Info("waiting for clickhouse replicas", "expected", replicaCount)
	replicas, err := waitForReplicas(serviceName, replicaCount, 10*time.Minute)
	if err != nil {
		shutdown.Fatal(err)
	}

	process := clickhouse.NewProcess()
	process.ReplicaName = fmt.Sprintf("replica-%d", replicaID)
	process.ClusterName = envString("CLICKHOUSE_CLUSTER", clickhouse.DefaultClusterName)
	process.AdvertisedHost = ip
	process.Password = os.Getenv("CLICKHOUSE_PASSWORD")
	process.KeeperServers = clickhouse.BuildKeeperServers(keepers, clickhouse.DefaultKeeperRaftPort)
	process.Replicas = clickhouse.BuildReplicas(replicas, clickhouse.DefaultNativePort)
	process.Logger = logger.New("component", "process", "replica", process.ReplicaName)

	logger.Info("starting clickhouse")
	if err := process.Start(); err != nil {
		shutdown.Fatal(err)
	}
	shutdown.BeforeExit(func() { process.Stop() })

	handler := clickhouse.NewHandler()
	handler.Process = process
	handler.Heartbeater = hb
	handler.Logger = logger.New("component", "http")

	logger.Info("serving admin api", "port", httpPort)
	shutdown.Fatal(http.ListenAndServe(":"+httpPort, handler))
}

func runAdmin(args []string) {
	if len(args) == 0 {
		shutdown.Fatal("usage: flynn-clickhouse admin clickhouse-client [args...]")
	}
	tool := args[0]
	rest := args[1:]

	if tool != "clickhouse-client" {
		toolPath := filepath.Join(clickhouse.DefaultBinDir, tool)
		if _, err := exec.LookPath(toolPath); err != nil {
			shutdown.Fatalf("unknown clickhouse tool %q: %s", tool, err)
		}
		if err := syscall.Exec(toolPath, append([]string{toolPath}, rest...), os.Environ()); err != nil {
			shutdown.Fatal(err)
		}
		return
	}

	host := os.Getenv("CLICKHOUSE_HOST")
	if host == "" {
		shutdown.Fatal("CLICKHOUSE_HOST is required")
	}
	port := os.Getenv("CLICKHOUSE_PORT")
	if port == "" {
		port = clickhouse.DefaultNativePort
	}

	final := []string{
		filepath.Join(clickhouse.DefaultBinDir, "clickhouse-client"),
		"--host", host,
		"--port", port,
	}
	if password := os.Getenv("CLICKHOUSE_PASSWORD"); password != "" {
		final = append(final, "--password", password)
	}
	final = append(final, rest...)

	if err := syscall.Exec(final[0], final, os.Environ()); err != nil {
		shutdown.Fatal(err)
	}
}

func keeperServiceName() string {
	serviceName := os.Getenv("FLYNN_CLICKHOUSE")
	if serviceName == "" {
		serviceName = "clickhouse"
	}
	return serviceName + "-keeper"
}

func waitForKeepers(service string, count int, timeout time.Duration) (map[int]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		insts, err := discoverd.GetInstances(service, 30*time.Second)
		if err != nil && time.Now().After(deadline) {
			return nil, err
		}

		keepers := make(map[int]string)
		for _, inst := range insts {
			idStr := inst.Meta[keeperServerIDKey]
			addr := inst.Meta[keeperRaftAddrKey]
			if idStr == "" || addr == "" {
				continue
			}
			id, err := strconv.Atoi(idStr)
			if err != nil {
				continue
			}
			keepers[id] = addr
		}

		if len(keepers) >= count {
			return keepers, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %d keeper nodes, found %d", count, len(keepers))
		}
		time.Sleep(time.Second)
	}
}

func waitForReplicas(service string, count int, timeout time.Duration) (map[int]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		insts, err := discoverd.GetInstances(service, 30*time.Second)
		if err != nil && time.Now().After(deadline) {
			return nil, err
		}

		replicas := make(map[int]string)
		for _, inst := range insts {
			idStr := inst.Meta[replicaIDKey]
			addr := inst.Meta[replicaNativeKey]
			if idStr == "" || addr == "" {
				continue
			}
			id, err := strconv.Atoi(idStr)
			if err != nil {
				continue
			}
			replicas[id] = addr
		}

		if len(replicas) >= count {
			return replicas, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %d clickhouse replicas, found %d", count, len(replicas))
		}
		time.Sleep(time.Second)
	}
}

func ipToNodeID(ip net.IP) int {
	ip = ip.To4()
	if ip == nil {
		return 1
	}
	return int(binary.BigEndian.Uint32([]byte(ip)) & 0x7fffffff)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
