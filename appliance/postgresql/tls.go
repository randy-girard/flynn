package postgresql

import (
	"os"
	"strings"
	"syscall"

	"github.com/randy-girard/flynn/pkg/tlscert"
)

const (
	tlsCertFile = "server.crt"
	tlsKeyFile  = "server.key"
	tlsCAFile   = "root.crt"
)

func defaultTLSHosts() []string {
	hosts := []string{"postgres.discoverd", "leader.postgres.discoverd", "postgres", "localhost"}
	for _, env := range []string{"DEFAULT_ROUTE_DOMAIN", "CLUSTER_DOMAIN"} {
		if d := os.Getenv(env); d != "" {
			hosts = append(hosts, "postgres."+d, "*."+d, d)
			break
		}
	}
	if extra := os.Getenv("TLS_HOSTS"); extra != "" {
		for _, h := range strings.Split(extra, ",") {
			if h = strings.TrimSpace(h); h != "" {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

func (p *Process) tlsCertPath() string { return p.dataPath(tlsCertFile) }
func (p *Process) tlsKeyPath() string  { return p.dataPath(tlsKeyFile) }
func (p *Process) tlsCAPath() string   { return p.dataPath(tlsCAFile) }

// ensureServerTLS writes server.crt/server.key/root.crt so postgres can speak
// TLS. Existing files are reused. In-cluster clients that still use
// sslmode=disable keep working because pg_hba uses host (not hostssl).
func (p *Process) ensureServerTLS() error {
	if p.dataDir == "" {
		return nil
	}
	certPath, keyPath, caPath := p.tlsCertPath(), p.tlsKeyPath(), p.tlsCAPath()
	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}

	if certPEM, keyPEM := os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"); certPEM != "" && keyPEM != "" {
		if err := p.writeTLSFile(certPath, certPEM, 0644); err != nil {
			return err
		}
		if err := p.writeTLSFile(keyPath, keyPEM, 0600); err != nil {
			return err
		}
		if ca := os.Getenv("CA_CERT"); ca != "" {
			return p.writeTLSFile(caPath, ca, 0644)
		}
		return nil
	}

	hosts := p.tlsHosts
	if len(hosts) == 0 {
		hosts = defaultTLSHosts()
	}
	c, err := tlscert.Generate(hosts)
	if err != nil {
		return err
	}
	if err := p.writeTLSFile(certPath, c.Cert, 0644); err != nil {
		return err
	}
	if err := p.writeTLSFile(keyPath, c.PrivateKey, 0600); err != nil {
		return err
	}
	if c.CACert != "" {
		return p.writeTLSFile(caPath, c.CACert, 0644)
	}
	return nil
}

// writeTLSFile writes path then chowns it to the data directory owner.
// Unit tests run postgres via sudo -u postgres while the Go process is root;
// a 0600 key owned by root makes postgres exit with "Permission denied".
func (p *Process) writeTLSFile(path, contents string, perm os.FileMode) error {
	if err := os.WriteFile(path, []byte(contents), perm); err != nil {
		return err
	}
	return p.matchDataDirOwner(path)
}

func (p *Process) matchDataDirOwner(path string) error {
	if p.dataDir == "" {
		return nil
	}
	st, err := os.Stat(p.dataDir)
	if err != nil {
		return nil
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if os.Geteuid() == int(sys.Uid) {
		return nil
	}
	if err := os.Chown(path, int(sys.Uid), int(sys.Gid)); err != nil && !os.IsPermission(err) {
		return err
	}
	return nil
}

func (p *Process) sslEnabled() bool {
	return fileExists(p.tlsCertPath()) && fileExists(p.tlsKeyPath())
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
