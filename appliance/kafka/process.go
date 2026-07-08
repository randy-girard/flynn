package kafka

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/inconshreveable/log15"
)

const (
	// DefaultPort is the port the broker listens on for client connections
	// (the CLIENT listener, which is SSL when TLS is enabled).
	DefaultPort = "9092"

	// DefaultControllerPort is the port used for the KRaft controller quorum
	// (the CONTROLLER listener, always PLAINTEXT and internal-only).
	DefaultControllerPort = "9093"

	// DefaultInternalPort is the port used for inter-broker traffic (the
	// INTERNAL listener, always PLAINTEXT and internal-only).
	DefaultInternalPort = "9094"

	// DefaultBinDir is the directory containing the Kafka shell tools.
	DefaultBinDir = "/opt/kafka/bin"

	// DefaultDataDir is the base directory for the log segments.
	DefaultDataDir = "/data"

	// DefaultOpTimeout is the default timeout for administrative operations.
	DefaultOpTimeout = 2 * time.Minute

	checkInterval = 500 * time.Millisecond
)

var (
	// ErrRunning is returned when starting an already running process.
	ErrRunning = errors.New("kafka already running")

	// ErrStopped is returned when stopping an already stopped process.
	ErrStopped = errors.New("kafka already stopped")

	// ErrTimeout is returned when an operation times out.
	ErrTimeout = errors.New("timeout")
)

// Process represents a running Kafka (KRaft) broker.
type Process struct {
	mtx     sync.Mutex
	running bool

	stopping atomic.Value
	stopped  chan struct{}

	cmd *exec.Cmd

	// NodeID is the unique KRaft node identifier for this broker.
	NodeID int

	// ClusterID is the shared KRaft cluster identifier. Every broker in the
	// same cluster must be formatted with the same value.
	ClusterID string

	// QuorumVoters is the controller.quorum.voters value, e.g.
	// "1@host-a:9093,2@host-b:9093".
	QuorumVoters string

	// AdvertisedHost is the address other clients/brokers use to reach us.
	AdvertisedHost string

	Port           string
	ControllerPort string
	InternalPort   string
	BinDir         string
	DataDir        string

	// TLS configuration for the CLIENT listener. When TLSEnabled is true the
	// client-facing listener uses SSL with mutual authentication; inter-broker
	// and controller traffic always remain PLAINTEXT on the internal network.
	TLSEnabled         bool
	KeystorePath       string
	TruststorePath     string
	KeystorePassword   string
	TruststorePassword string

	// CommandConfigPath points at a client.properties file passed to the
	// kafka-*.sh admin tools via --command-config when TLS is enabled.
	CommandConfigPath string

	// ReplicationFactor is applied to the internal topics and used as the
	// default replication factor for user topics.
	ReplicationFactor int

	// MinInSyncReplicas is the default min.insync.replicas for durability.
	MinInSyncReplicas int

	// NumPartitions is the default partition count for new topics.
	NumPartitions int

	Singleton bool
	OpTimeout time.Duration
	Logger    log15.Logger
}

// NewProcess returns a new instance of Process with defaults.
func NewProcess() *Process {
	p := &Process{
		Port:              DefaultPort,
		ControllerPort:    DefaultControllerPort,
		InternalPort:      DefaultInternalPort,
		BinDir:            DefaultBinDir,
		DataDir:           DefaultDataDir,
		ReplicationFactor: 1,
		MinInSyncReplicas: 1,
		NumPartitions:     3,
		OpTimeout:         DefaultOpTimeout,
		Logger:            log15.New("app", "kafka"),
	}
	p.stopping.Store(false)
	return p
}

// LogDir returns the path to the KRaft log directory.
func (p *Process) LogDir() string { return filepath.Join(p.DataDir, "kraft-logs") }

// ConfigPath returns the path to the generated server.properties.
func (p *Process) ConfigPath() string { return filepath.Join(p.DataDir, "server.properties") }

// BootstrapServer returns the local broker address used by admin tools.
func (p *Process) BootstrapServer() string { return "localhost:" + p.Port }

// ClientSecurityProtocol returns the security protocol for the CLIENT listener.
func (p *Process) ClientSecurityProtocol() string {
	if p.TLSEnabled {
		return "SSL"
	}
	return "PLAINTEXT"
}

// Start generates configuration, formats storage if required, and launches the
// broker. It returns ErrRunning if the process is already running.
func (p *Process) Start() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.running {
		return ErrRunning
	}
	return p.start()
}

func (p *Process) start() error {
	logger := p.Logger.New("fn", "start", "node.id", p.NodeID, "data_dir", p.DataDir)

	p.stopping.Store(false)
	p.stopped = make(chan struct{})

	if err := p.writeConfig(); err != nil {
		logger.Error("error writing config", "path", p.ConfigPath(), "err", err)
		return err
	}

	if err := p.formatStorage(); err != nil {
		logger.Error("error formatting storage", "err", err)
		return err
	}

	logger.Info("starting process")

	cmd := exec.Command(filepath.Join(p.BinDir, "kafka-server-start.sh"), p.ConfigPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Error("failed to start", "err", err)
		return err
	}
	p.cmd = cmd
	p.running = true

	go p.monitorCmd(p.cmd, p.stopped)

	// Wait until the broker API is accepting connections.
	if err := p.waitReady(p.OpTimeout); err != nil {
		return err
	}

	logger.Info("process started")
	return nil
}

// formatStorage runs kafka-storage.sh to format the log directory the first
// time the broker boots. Formatting is idempotent-safe here because we guard on
// the presence of the meta.properties marker.
func (p *Process) formatStorage() error {
	logger := p.Logger.New("fn", "formatStorage")

	if _, err := os.Stat(filepath.Join(p.LogDir(), "meta.properties")); err == nil {
		logger.Info("storage already formatted")
		return nil
	}

	if err := os.MkdirAll(p.LogDir(), 0755); err != nil {
		return err
	}

	logger.Info("formatting storage", "cluster.id", p.ClusterID)
	out, err := exec.Command(
		filepath.Join(p.BinDir, "kafka-storage.sh"), "format",
		"--cluster-id", p.ClusterID,
		"--config", p.ConfigPath(),
		"--ignore-formatted",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("format storage: %s: %s", err, out)
	}
	return nil
}

// Stop attempts a graceful shutdown, escalating to SIGKILL on timeout.
func (p *Process) Stop() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if !p.running {
		return ErrStopped
	}
	return p.stop()
}

func (p *Process) stop() error {
	logger := p.Logger.New("fn", "stop")
	logger.Info("stopping")

	p.stopping.Store(true)

	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		logger.Debug("signalling daemon", "sig", sig)
		if err := p.cmd.Process.Signal(sig); err != nil {
			logger.Error("error signalling daemon", "sig", sig, "err", err)
		}

		select {
		case <-time.After(p.OpTimeout):
			continue
		case <-p.stopped:
			p.running = false
			return nil
		}
	}
	return errors.New("unable to kill kafka")
}

func (p *Process) monitorCmd(cmd *exec.Cmd, stopped chan struct{}) {
	err := cmd.Wait()
	if !p.stopping.Load().(bool) {
		p.Logger.Error("unexpectedly exit", "err", err)
		shutdown.ExitWithCode(1)
	}
	close(stopped)
}

// writeConfig generates server.properties from the current configuration.
func (p *Process) writeConfig() error {
	if err := os.MkdirAll(p.DataDir, 0755); err != nil {
		return err
	}

	f, err := os.Create(p.ConfigPath())
	if err != nil {
		return err
	}
	defer f.Close()

	return configTemplate.Execute(f, p)
}

// RenderConfig returns the generated server.properties contents. It is useful
// for inspection and testing without starting a broker.
func (p *Process) RenderConfig() (string, error) {
	var buf bytes.Buffer
	if err := configTemplate.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Info returns runtime information about the process.
func (p *Process) Info() (*ProcessInfo, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return &ProcessInfo{
		Running:   p.running,
		NodeID:    p.NodeID,
		ClusterID: p.ClusterID,
	}, nil
}

// ProcessInfo represents state about the process returned by Process.Info().
type ProcessInfo struct {
	Running   bool   `json:"running"`
	NodeID    int    `json:"node_id"`
	ClusterID string `json:"cluster_id"`
}

// waitReady polls the broker API until it responds or the timeout elapses.
func (p *Process) waitReady(timeout time.Duration) error {
	logger := p.Logger.New("fn", "waitReady")

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		if _, err := p.run(p.OpTimeout, "kafka-broker-api-versions.sh",
			p.adminArgs()...); err == nil {
			return nil
		} else {
			logger.Debug("broker not ready", "err", err)
		}

		select {
		case <-timer.C:
			return ErrTimeout
		case <-ticker.C:
		}
	}
}

// adminArgs builds a common argument list for the kafka-*.sh admin tools:
// the bootstrap server plus, when TLS is enabled, the --command-config file
// that carries the SSL client settings.
func (p *Process) adminArgs(args ...string) []string {
	base := []string{"--bootstrap-server", p.BootstrapServer()}
	base = append(base, args...)
	if p.CommandConfigPath != "" {
		base = append(base, "--command-config", p.CommandConfigPath)
	}
	return base
}

// run executes a Kafka shell tool and returns its combined output.
func (p *Process) run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(filepath.Join(p.BinDir, name), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-time.After(timeout):
		cmd.Process.Kill()
		return buf.Bytes(), ErrTimeout
	case err := <-done:
		if err != nil {
			return buf.Bytes(), fmt.Errorf("%s: %s", err, strings.TrimSpace(buf.String()))
		}
		return buf.Bytes(), nil
	}
}

// BuildQuorumVoters constructs a controller.quorum.voters value from a set of
// (nodeID -> controllerAddr) pairs. The output is deterministically ordered by
// node id so every broker generates an identical value.
func BuildQuorumVoters(voters map[int]string) string {
	ids := make([]int, 0, len(voters))
	for id := range voters {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf("%d@%s", id, voters[id]))
	}
	return strings.Join(parts, ",")
}

func itoa(i int) string { return strconv.Itoa(i) }

var configTemplate = template.Must(template.New("server.properties").Parse(`
process.roles=broker,controller
node.id={{.NodeID}}
controller.quorum.voters={{.QuorumVoters}}

# Three listeners: CLIENT is what apps/external clients connect to (SSL when TLS
# is enabled); INTERNAL (inter-broker) and CONTROLLER stay PLAINTEXT on the
# private cluster network.
listeners=CLIENT://:{{.Port}},INTERNAL://:{{.InternalPort}},CONTROLLER://:{{.ControllerPort}}
advertised.listeners=CLIENT://{{.AdvertisedHost}}:{{.Port}},INTERNAL://{{.AdvertisedHost}}:{{.InternalPort}}
inter.broker.listener.name=INTERNAL
controller.listener.names=CONTROLLER
listener.security.protocol.map=CONTROLLER:PLAINTEXT,INTERNAL:PLAINTEXT,CLIENT:{{.ClientSecurityProtocol}}
{{if .TLSEnabled}}
# CLIENT listener mutual TLS.
ssl.keystore.location={{.KeystorePath}}
ssl.keystore.password={{.KeystorePassword}}
ssl.keystore.type=PKCS12
ssl.truststore.location={{.TruststorePath}}
ssl.truststore.password={{.TruststorePassword}}
ssl.truststore.type=PKCS12
ssl.client.auth=required
# Brokers advertise dynamic per-node IPs; clients authenticate via the shared CA
# rather than hostname, so endpoint identification is disabled.
ssl.endpoint.identification.algorithm=
{{end}}
log.dirs={{.LogDir}}

# Topics must be created explicitly through the flynn kafka CLI.
auto.create.topics.enable=false
delete.topic.enable=true

num.partitions={{.NumPartitions}}
default.replication.factor={{.ReplicationFactor}}
offsets.topic.replication.factor={{.ReplicationFactor}}
transaction.state.log.replication.factor={{.ReplicationFactor}}
transaction.state.log.min.isr={{.MinInSyncReplicas}}
min.insync.replicas={{.MinInSyncReplicas}}
`[1:]))
