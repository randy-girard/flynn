package redis

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/garyburd/redigo/redis"
	"github.com/inconshreveable/log15"
)

const (
	DefaultPort        = "6379"
	DefaultBinDir      = "/bin"
	DefaultDataDir     = "/data"
	DefaultPassword    = ""
	DefaultOpTimeout   = 5 * time.Minute
	DefaultReplTimeout = 1 * time.Minute

	checkInterval = 100 * time.Millisecond
)

var (
	// ErrTimeout is returned when an operation times out.
	ErrTimeout = errors.New("timeout")

	// ErrRunning is returned when starting an already running process.
	ErrRunning = errors.New("redis already running")

	// ErrStopped is returned when stopping an already stopped process.
	ErrStopped = errors.New("redis already stopped")
)

// Process represents a running Redis process.
type Process struct {
	mtx     sync.Mutex
	running bool

	stopping atomic.Value
	stopped  chan struct{}

	// The daemon running redis-server.
	cmd *exec.Cmd

	ID           string
	Singleton    bool
	Port         string
	BinDir       string
	DataDir      string
	Password     string
	OpTimeout    time.Duration
	ReplTimeout  time.Duration
	Logger       log15.Logger
	WaitUpstream bool
}

// NewProcess returns a new instance of Process with defaults.
func NewProcess() *Process {
	p := &Process{
		Port:        DefaultPort,
		BinDir:      DefaultBinDir,
		DataDir:     DefaultDataDir,
		Password:    DefaultPassword,
		OpTimeout:   DefaultOpTimeout,
		ReplTimeout: DefaultReplTimeout,
		Logger:      log15.New("app", "redis"),
	}
	p.stopping.Store(false)
	return p
}

// ConfigPath returns the path to the redis.conf.
func (p *Process) ConfigPath() string { return filepath.Join(p.DataDir, "redis.conf") }

// Start begins the process.
// Returns an error if the process is already running.
func (p *Process) Start() error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	// Valdiate that process is not already running and that we have a config.
	if p.running {
		return ErrRunning
	}
	return p.start()
}

func (p *Process) start() error {
	logger := p.Logger.New("fn", "start", "data_dir", p.DataDir, "bin_dir", p.BinDir)

	// Initialize stop state so we know if it's an expected stop.
	p.stopping.Store(false)
	p.stopped = make(chan struct{})

	// Generate configuration.
	if err := p.writeConfig(); err != nil {
		logger.Error("error writing conf", "path", p.ConfigPath(), "err", err)
		return err
	}

	logger.Info("starting process")

	// Execute redis-server binary.
	cmd := exec.Command(filepath.Join(p.BinDir, "redis-server"), p.ConfigPath())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		logger.Error("failed to start", "err", err)
		return err
	}
	p.cmd = cmd
	p.running = true

	logger.Info("process started")

	// Monitor redis process to check for unexpected quit.
	go p.monitorCmd(p.cmd, p.stopped)

	// Ping until successful response.
	if err := p.pingWait("", p.OpTimeout); err != nil {
		return err
	}

	return nil
}

// Stop attempts to gracefully stop the process. If the process cannot be
// stopped gracefully then it is forcefully stopped. Returns an error if the
// process is already stopped.
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

	// Mark process as expecting a shutdown so monitorCmd does not treat the
	// exit as a crash (which calls shutdown.ExitWithCode).
	p.stopping.Store(true)

	// Prefer SHUTDOWN SAVE so the RDB/AOF is flushed before the daemon exits.
	// flynn-host only waits 30s after SIGTERM before SIGKILL of the container.
	stopWait := 10 * time.Second
	if err := p.shutdownSave(); err != nil {
		logger.Info("SHUTDOWN SAVE not accepted, signalling", "err", err)
	} else {
		select {
		case <-p.stopped:
			p.running = false
			return nil
		case <-time.After(stopWait):
			logger.Info("SHUTDOWN SAVE timed out")
		}
	}

	for _, sig := range []os.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		logger.Debug("signalling daemon", "sig", sig)
		if err := p.cmd.Process.Signal(sig); err != nil {
			logger.Error("error signalling daemon", "sig", sig, "err", err)
		}

		select {
		case <-time.After(stopWait):
			continue
		case <-p.stopped:
			p.running = false
			return nil
		}
	}
	return errors.New("unable to kill redis")
}

func (p *Process) shutdownSave() error {
	addr := net.JoinHostPort("127.0.0.1", p.Port)
	conn, err := redis.Dial("tcp", addr,
		redis.DialPassword(p.Password),
		redis.DialConnectTimeout(2*time.Second),
		redis.DialReadTimeout(10*time.Second),
		redis.DialWriteTimeout(2*time.Second),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Do("SHUTDOWN", "SAVE")
	if shutdownSaveAccepted(err) {
		return nil
	}
	return err
}

// shutdownSaveAccepted reports whether err from SHUTDOWN SAVE is success.
// Redis closes the connection as it exits, so EOF / "connection" errors are
// the normal completion path — not a failed persist.
func shutdownSaveAccepted(err error) bool {
	if err == nil || err == io.EOF {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection") || strings.Contains(msg, "EOF") || strings.Contains(msg, "broken pipe")
}

// monitorCmd waits for cmd to finish and reports an error if it was unexpected.
// Also closes the stopped channel to notify other listeners that it has finished.
func (p *Process) monitorCmd(cmd *exec.Cmd, stopped chan struct{}) {
	err := cmd.Wait()
	if !p.stopping.Load().(bool) {
		p.Logger.Error("unexpectedly exit", "err", err)
		shutdown.ExitWithCode(1)
	}
	close(stopped)
}

// writeConfig generates a new redis.conf at the config path.
func (p *Process) writeConfig() error {
	logger := p.Logger.New("fn", "writeConfig")
	logger.Info("writing")

	// Create parent directory if it doesn't exist.
	if err := os.MkdirAll(filepath.Dir(p.ConfigPath()), 0777); err != nil {
		logger.Error("cannot create config parent directory", "err", err)
		return err
	}

	f, err := os.Create(p.ConfigPath())
	if err != nil {
		logger.Error("cannot create config file", "err", err)
		return err
	}
	defer f.Close()

	return configTemplate.Execute(f, struct {
		ID       string
		Port     string
		DataDir  string
		Password string
	}{p.ID, p.Port, p.DataDir, p.Password})
}

// Info returns information about the process.
func (p *Process) Info() (*ProcessInfo, error) {
	p.mtx.Lock()
	defer p.mtx.Unlock()
	return &ProcessInfo{Running: p.running}, nil
}

// ProcessInfo represents state about the process returned by Process.Info().
type ProcessInfo struct {
	Running bool `json:"running"`
}

// ping executes a PING command against addr until timeout occurs.
func (p *Process) ping(addr string, timeout time.Duration) error {
	// Default to local process if addr not specified.
	if addr == "" {
		addr = fmt.Sprintf("localhost:%s", p.Port)
	}

	logger := p.Logger.New("fn", "ping", "addr", addr, "timeout", timeout)
	logger.Info("sending")

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		// Attempt to ping the server.
		if ok := func() bool {
			logger.Info("sending PING")

			conn, err := redis.Dial("tcp", addr,
				redis.DialPassword(p.Password),
				redis.DialConnectTimeout(timeout),
				redis.DialReadTimeout(timeout),
				redis.DialWriteTimeout(timeout),
			)
			if err != nil {
				logger.Error("conn error", "err", err)
				return false
			}
			defer conn.Close()

			if _, err := conn.Do("PING"); err != nil {
				logger.Error("error getting upstream status", "err", err)
				return false
			}

			logger.Info("PONG received")
			return true
		}(); ok {
			return nil
		}

		select {
		case <-timer.C:
			logger.Info("timeout")
			return ErrTimeout
		case <-ticker.C:
		}
	}
}

// pingWait continually pings a server until successful response or timeout.
func (p *Process) pingWait(addr string, timeout time.Duration) error {
	// Default to local process if addr not specified.
	if addr == "" {
		addr = fmt.Sprintf("localhost:%s", p.Port)
	}

	logger := p.Logger.New("fn", "pingWait", "addr", addr, "timeout", timeout)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return ErrTimeout
		case <-ticker.C:
		}

		if err := p.ping(addr, timeout); err != nil {
			logger.Error("ping error", "err", err)
			continue
		}

		return nil
	}
}

// Restore stops the process, copies an RDB from r, and restarts the process.
// Redis automatically handles recovery when there's a dump.rdb file present.
func (p *Process) Restore(r io.Reader) error {
	p.mtx.Lock()
	defer p.mtx.Unlock()

	logger := p.Logger.New("fn", "Restore")
	logger.Info("begin restore")

	// Stop if running.
	if p.running {
		logger.Info("stopping process")
		if err := p.stop(); err != nil {
			logger.Error("error stopping process", "err", err)
			return err
		}
	}

	// Create dump file in data directory.
	logger.Info("copying dump.rdb")
	if err := func() error {
		f, err := os.Create(filepath.Join(p.DataDir, "dump.rdb"))
		if err != nil {
			logger.Error("error creating dump file", "err", err)
			return err
		}
		defer f.Close()

		// Copy from reader to dump file.
		n, err := io.Copy(f, r)
		if err != nil {
			logger.Error("error creating dump file", "err", err)
			return err
		}
		logger.Info("copy completed", "n", n)

		return nil
	}(); err != nil {
		return err
	}

	// Restart process.
	if err := p.start(); err != nil {
		logger.Error("error restarting process", "err", err)
		return err
	}

	return nil
}

// RedisInfo executes an INFO command against a Redis server and returns the results.
func (p *Process) RedisInfo(addr string, timeout time.Duration) (*RedisInfo, error) {
	// Default to local process if addr not specified.
	if addr == "" {
		addr = fmt.Sprintf("localhost:%s", p.Port)
	}

	logger := p.Logger.New("fn", "replInfo", "addr", addr)
	logger.Info("sending INFO")

	// Connect to the redis server.
	conn, err := redis.Dial("tcp", addr,
		redis.DialPassword(p.Password),
		redis.DialConnectTimeout(timeout),
		redis.DialReadTimeout(timeout),
		redis.DialWriteTimeout(timeout),
	)
	if err != nil {
		logger.Info("dial error", "err", err)
		return nil, err
	}
	defer conn.Close()

	// Execute INFO command.
	reply, err := conn.Do("INFO")
	if err != nil {
		logger.Error("info error", "err", err)
		return nil, err
	}

	buf, ok := reply.([]byte)
	if !ok {
		logger.Error("info reply type error", "type", fmt.Sprintf("%T", buf))
		return nil, fmt.Errorf("unexpected INFO reply format: %T", buf)
	}

	// Parse the bulk string reply info a typed object.
	info, err := ParseRedisInfo(string(buf))
	if err != nil {
		logger.Error("parse info error", "err", err)
		return nil, fmt.Errorf("parse info: %s", err)
	}

	logger.Info("INFO received")
	return info, nil
}

// RedisInfo represents the reply from an INFO command. Not all fields are listed.
type RedisInfo struct {
	Role string // role

	MasterHost       string        // master_host
	MasterPort       int           // master_port
	MasterLinkStatus string        // master_link_status
	MasterLastIO     time.Duration // master_last_io_seconds_ago

	MasterSyncInProgress bool          // master_sync_in_progress
	MasterSyncLeftBytes  int64         // master_sync_left_bytes
	MasterSyncLastIO     time.Duration // master_sync_last_io_seconds_ago
	MasterLinkDownSince  time.Duration // master_link_down_since_seconds

	ConnectedSlaves int      // connected_slaves
	Slaves          []string // slaveXXX
}

// ParseRedisInfo parses the response from an INFO command.
func ParseRedisInfo(s string) (*RedisInfo, error) {
	var info RedisInfo

	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := scanner.Text()

		// Skip blank lines & comment lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		// Split into key/value.
		a := strings.SplitN(line, ":", 2)
		if len(a) < 2 {
			continue
		}
		key, value := strings.TrimSpace(a[0]), strings.TrimSpace(a[1])

		// Parse into appropriate field.
		switch key {
		case "role":
			info.Role = value
		case "master_host":
			info.MasterHost = value
		case "master_port":
			info.MasterPort = atoi(value)
		case "master_link_status":
			info.MasterLinkStatus = value
		case "master_last_io_seconds_ago":
			info.MasterLastIO = time.Duration(atoi(value)) * time.Second
		case "master_sync_in_progress":
			info.MasterSyncInProgress = value == "1"
		case "master_sync_left_bytes":
			info.MasterSyncLeftBytes, _ = strconv.ParseInt(value, 10, 64)
		case "master_sync_last_io_seconds_ago":
			info.MasterSyncLastIO = time.Duration(atoi(value)) * time.Second
		case "master_link_down_since_seconds":
			info.MasterLinkDownSince = time.Duration(atoi(value)) * time.Second
		case "connected_slaves":
			info.ConnectedSlaves = atoi(value)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return &info, nil
}

var configTemplate = template.Must(template.New("redis.conf").Parse(`
port {{.Port}}
dbfilename dump.rdb
dir {{.DataDir}}

# AOF so SETs survive a non-graceful kill (default RDB save 900 1 is too slow
# for flynn-host update). appendfsync everysec loses at most ~1s of writes.
appendonly yes
appendfsync everysec

# slaveof <masterip> <masterport>
# masterauth <master-password>
slave-serve-stale-data yes
slave-read-only yes

requirepass "{{.Password}}"
`[1:]))

// atoi returns the parsed integer value of s. Returns zero if a parse error occurs.
func atoi(s string) int {
	i, _ := strconv.Atoi(s)
	return i
}
