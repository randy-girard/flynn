package clickhouse

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/inconshreveable/log15"
)

const (
	// DefaultHTTPPort is the HTTP interface port.
	DefaultHTTPPort = "8123"

	// DefaultNativePort is the native protocol port.
	DefaultNativePort = "9000"

	// DefaultInterserverPort is the port used for replicated table traffic.
	DefaultInterserverPort = "9009"

	// DefaultBinDir is the directory containing ClickHouse binaries.
	DefaultBinDir = "/usr/bin"

	// DefaultDataDir is the base directory for server data.
	DefaultDataDir = "/data"

	// DefaultClusterName is the remote_servers cluster name used for ON CLUSTER DDL.
	DefaultClusterName = "flynn"

	// DefaultOpTimeout is the default timeout for administrative operations.
	DefaultOpTimeout = 2 * time.Minute

	checkInterval = 500 * time.Millisecond
)

var (
	// ErrRunning is returned when starting an already running process.
	ErrRunning = errors.New("clickhouse already running")

	// ErrStopped is returned when stopping an already stopped process.
	ErrStopped = errors.New("clickhouse already stopped")

	// ErrTimeout is returned when an operation times out.
	ErrTimeout = errors.New("timeout")
)

// Replica describes a ClickHouse replica in the cluster definition.
type Replica struct {
	Host string
	Port string
}

// Process represents a running clickhouse-server process.
type Process struct {
	mtx     sync.Mutex
	running bool

	stopping atomic.Value
	stopped  chan struct{}

	cmd *exec.Cmd

	ReplicaName string
	ShardName   string
	ClusterName string

	HTTPPort         string
	NativePort       string
	InterserverPort  string
	AdvertisedHost   string
	BinDir           string
	DataDir          string
	Password         string
	KeeperServers    []KeeperServer
	KeeperClientPort string
	Replicas         []Replica
	OpTimeout        time.Duration
	Logger           log15.Logger
}

// NewProcess returns a new Process with defaults.
func NewProcess() *Process {
	p := &Process{
		ShardName:       "01",
		ClusterName:     DefaultClusterName,
		HTTPPort:        DefaultHTTPPort,
		NativePort:      DefaultNativePort,
		InterserverPort: DefaultInterserverPort,
		BinDir:          DefaultBinDir,
		DataDir:         DefaultDataDir,
		KeeperClientPort: DefaultKeeperClientPort,
		OpTimeout:       DefaultOpTimeout,
		Logger:          log15.New("app", "clickhouse"),
	}
	p.stopping.Store(false)
	return p
}

// ConfigDir returns the path to generated configuration fragments.
func (p *Process) ConfigDir() string { return filepath.Join(p.DataDir, "config.d") }

// ConfigPath returns the main config file path passed to clickhouse-server.
func (p *Process) ConfigPath() string { return filepath.Join(p.ConfigDir(), "flynn.xml") }

// NativeAddr returns the local native protocol address for admin tools.
func (p *Process) NativeAddr() string { return "localhost:" + p.NativePort }

// Start generates configuration and launches clickhouse-server.
func (p *Process) Start() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.running {
		return ErrRunning
	}
	return p.start()
}

func (p *Process) start() error {
	logger := p.Logger.New("fn", "start", "replica", p.ReplicaName)

	p.stopping.Store(false)
	p.stopped = make(chan struct{})

	if err := p.writeConfig(); err != nil {
		logger.Error("error writing config", "err", err)
		return err
	}

	logger.Info("starting process")
	cmd := exec.Command(filepath.Join(p.BinDir, "clickhouse-server"), "--config-file", p.ConfigPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Error("failed to start", "err", err)
		return err
	}
	p.cmd = cmd
	p.running = true

	go p.monitorCmd(p.cmd, p.stopped)

	if err := p.waitReady(p.OpTimeout); err != nil {
		return err
	}

	logger.Info("process started")
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
	return errors.New("unable to kill clickhouse")
}

func (p *Process) monitorCmd(cmd *exec.Cmd, stopped chan struct{}) {
	err := cmd.Wait()
	if !p.stopping.Load().(bool) {
		p.Logger.Error("unexpectedly exit", "err", err)
		shutdown.ExitWithCode(1)
	}
	close(stopped)
}

func (p *Process) writeConfig() error {
	if err := os.MkdirAll(p.ConfigDir(), 0755); err != nil {
		return err
	}

	f, err := os.Create(p.ConfigPath())
	if err != nil {
		return err
	}
	defer f.Close()

	return serverConfigTemplate.Execute(f, p)
}

// RenderConfig returns the generated server configuration for inspection and tests.
func (p *Process) RenderConfig() (string, error) {
	var buf bytes.Buffer
	if err := serverConfigTemplate.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Info returns runtime information about the process.
func (p *Process) Info() (*ProcessInfo, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return &ProcessInfo{
		Running:     p.running,
		ReplicaName: p.ReplicaName,
		ClusterName: p.ClusterName,
	}, nil
}

// ProcessInfo represents state returned by Process.Info().
type ProcessInfo struct {
	Running     bool   `json:"running"`
	ReplicaName string `json:"replica_name"`
	ClusterName string `json:"cluster_name"`
}

func (p *Process) waitReady(timeout time.Duration) error {
	logger := p.Logger.New("fn", "waitReady")

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		if _, err := p.run(p.OpTimeout, "clickhouse-client", p.clientArgs("--query", "SELECT 1")...); err == nil {
			return nil
		} else {
			logger.Debug("server not ready", "err", err)
		}

		select {
		case <-timer.C:
			return ErrTimeout
		case <-ticker.C:
		}
	}
}

func (p *Process) clientArgs(args ...string) []string {
	base := []string{
		"--host", "localhost",
		"--port", p.NativePort,
	}
	if p.Password != "" {
		base = append(base, "--password", p.Password)
	}
	return append(base, args...)
}

// run executes a ClickHouse binary and returns its combined output.
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
			return buf.Bytes(), fmt.Errorf("%s: %s", err, bytes.TrimSpace(buf.Bytes()))
		}
		return buf.Bytes(), nil
	}
}

// BuildReplicas constructs a deterministic replica list from host -> native port.
func BuildReplicas(hosts map[int]string, port string) []Replica {
	ids := make([]int, 0, len(hosts))
	for id := range hosts {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	out := make([]Replica, 0, len(ids))
	for _, id := range ids {
		host := hosts[id]
		if i := indexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		out = append(out, Replica{Host: host, Port: port})
	}
	return out
}

var serverConfigTemplate = template.Must(template.New("flynn.xml").Parse(`
<clickhouse>
    <logger>
        <level>information</level>
        <console>true</console>
    </logger>

    <path>{{.DataDir}}/store/</path>
    <tmp_path>{{.DataDir}}/tmp/</tmp_path>
    <user_files_path>{{.DataDir}}/user_files/</user_files_path>
    <format_schema_path>{{.DataDir}}/format_schemas/</format_schema_path>

    <http_port>{{.HTTPPort}}</http_port>
    <tcp_port>{{.NativePort}}</tcp_port>
    <interserver_http_port>{{.InterserverPort}}</interserver_http_port>
    <interserver_http_host>{{.AdvertisedHost}}</interserver_http_host>
    <listen_host>0.0.0.0</listen_host>

    <users>
        <default>
            <password>{{.Password}}</password>
            <networks>
                <ip>::/0</ip>
            </networks>
            <profile>default</profile>
            <quota>default</quota>
        </default>
    </users>

    <zookeeper>
{{- range .KeeperServers }}
        <node>
            <host>{{ .Host }}</host>
            <port>{{ $.KeeperClientPort }}</port>
        </node>
{{- end }}
    </zookeeper>

    <remote_servers>
        <{{.ClusterName}}>
            <shard>
                <internal_replication>true</internal_replication>
{{- range .Replicas }}
                <replica>
                    <host>{{ .Host }}</host>
                    <port>{{ .Port }}</port>
                </replica>
{{- end }}
            </shard>
        </{{.ClusterName}}>
    </remote_servers>

    <macros>
        <cluster>{{.ClusterName}}</cluster>
        <shard>{{.ShardName}}</shard>
        <replica>{{.ReplicaName}}</replica>
    </macros>
</clickhouse>
`[1:]))
