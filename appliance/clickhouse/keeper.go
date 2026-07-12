package clickhouse

import (
	"bytes"
	"errors"
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
	// DefaultKeeperClientPort is the port ClickHouse servers use to reach Keeper.
	DefaultKeeperClientPort = "9181"

	// DefaultKeeperRaftPort is the port Keeper uses for its Raft quorum.
	DefaultKeeperRaftPort = "9234"

	// DefaultKeeperBin is the clickhouse-keeper binary name.
	DefaultKeeperBin = "clickhouse-keeper"
)

var (
	// ErrKeeperRunning is returned when starting an already running keeper.
	ErrKeeperRunning = errors.New("clickhouse-keeper already running")

	// ErrKeeperStopped is returned when stopping an already stopped keeper.
	ErrKeeperStopped = errors.New("clickhouse-keeper already stopped")
)

// KeeperServer describes a single node in the Keeper Raft configuration.
type KeeperServer struct {
	ID   int
	Host string
	Port string
}

// KeeperProcess represents a running clickhouse-keeper process.
type KeeperProcess struct {
	mtx     sync.Mutex
	running bool

	stopping atomic.Value
	stopped  chan struct{}

	cmd *exec.Cmd

	ServerID  int
	Servers   []KeeperServer
	ClientPort string
	RaftPort   string
	BinDir     string
	DataDir    string
	OpTimeout  time.Duration
	Logger     log15.Logger
}

// NewKeeperProcess returns a new KeeperProcess with defaults.
func NewKeeperProcess() *KeeperProcess {
	p := &KeeperProcess{
		ClientPort: DefaultKeeperClientPort,
		RaftPort:   DefaultKeeperRaftPort,
		BinDir:     DefaultBinDir,
		DataDir:    DefaultDataDir,
		OpTimeout:  DefaultOpTimeout,
		Logger:     log15.New("app", "clickhouse-keeper"),
	}
	p.stopping.Store(false)
	return p
}

// ConfigPath returns the path to the generated keeper config.
func (p *KeeperProcess) ConfigPath() string { return filepath.Join(p.DataDir, "keeper_config.xml") }

// Start generates configuration and launches clickhouse-keeper.
func (p *KeeperProcess) Start() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if p.running {
		return ErrKeeperRunning
	}
	return p.start()
}

func (p *KeeperProcess) start() error {
	logger := p.Logger.New("fn", "start", "server.id", p.ServerID)

	p.stopping.Store(false)
	p.stopped = make(chan struct{})

	if err := p.writeConfig(); err != nil {
		logger.Error("error writing config", "err", err)
		return err
	}

	logger.Info("starting keeper")
	cmd := exec.Command(filepath.Join(p.BinDir, DefaultKeeperBin), "--config", p.ConfigPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Error("failed to start", "err", err)
		return err
	}
	p.cmd = cmd
	p.running = true

	go p.monitorCmd(p.cmd, p.stopped)
	return nil
}

// Stop attempts a graceful shutdown, escalating to SIGKILL on timeout.
func (p *KeeperProcess) Stop() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	if !p.running {
		return ErrKeeperStopped
	}
	return p.stop()
}

func (p *KeeperProcess) stop() error {
	logger := p.Logger.New("fn", "stop")
	logger.Info("stopping")

	p.stopping.Store(true)

	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := p.cmd.Process.Signal(sig); err != nil {
			logger.Error("error signalling keeper", "sig", sig, "err", err)
		}

		select {
		case <-time.After(p.OpTimeout):
			continue
		case <-p.stopped:
			p.running = false
			return nil
		}
	}
	return errors.New("unable to kill clickhouse-keeper")
}

func (p *KeeperProcess) monitorCmd(cmd *exec.Cmd, stopped chan struct{}) {
	err := cmd.Wait()
	if !p.stopping.Load().(bool) {
		p.Logger.Error("unexpectedly exit", "err", err)
		shutdown.ExitWithCode(1)
	}
	close(stopped)
}

func (p *KeeperProcess) writeConfig() error {
	if err := os.MkdirAll(p.DataDir, 0755); err != nil {
		return err
	}

	f, err := os.Create(p.ConfigPath())
	if err != nil {
		return err
	}
	defer f.Close()

	return keeperConfigTemplate.Execute(f, p)
}

// RenderKeeperConfig returns the generated keeper config for inspection and tests.
func (p *KeeperProcess) RenderKeeperConfig() (string, error) {
	var buf bytes.Buffer
	if err := keeperConfigTemplate.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Info returns runtime information about the keeper process.
func (p *KeeperProcess) Info() (*KeeperInfo, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return &KeeperInfo{Running: p.running, ServerID: p.ServerID}, nil
}

// KeeperInfo represents state returned by KeeperProcess.Info().
type KeeperInfo struct {
	Running  bool `json:"running"`
	ServerID int  `json:"server_id"`
}

// BuildKeeperServers constructs a deterministic KeeperServer list from a map of
// server id -> raft address host:port.
func BuildKeeperServers(servers map[int]string, raftPort string) []KeeperServer {
	ids := make([]int, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	out := make([]KeeperServer, 0, len(ids))
	for _, id := range ids {
		host := servers[id]
		if i := indexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		out = append(out, KeeperServer{ID: id, Host: host, Port: raftPort})
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

var keeperConfigTemplate = template.Must(template.New("keeper_config.xml").Parse(`
<clickhouse>
    <logger>
        <level>information</level>
        <console>true</console>
    </logger>
    <keeper_server>
        <tcp_port>{{.ClientPort}}</tcp_port>
        <server_id>{{.ServerID}}</server_id>
        <log_storage_path>{{.DataDir}}/coordination/log</log_storage_path>
        <snapshot_storage_path>{{.DataDir}}/coordination/snapshots</snapshot_storage_path>
        <raft_configuration>
{{- range .Servers }}
            <server>
                <id>{{ .ID }}</id>
                <hostname>{{ .Host }}</hostname>
                <port>{{ .Port }}</port>
            </server>
{{- end }}
        </raft_configuration>
    </keeper_server>
</clickhouse>
`[1:]))
