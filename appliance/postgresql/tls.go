package postgresql

import (
	"os"
	"strings"

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
		if err := os.WriteFile(certPath, []byte(certPEM), 0644); err != nil {
			return err
		}
		if err := os.WriteFile(keyPath, []byte(keyPEM), 0600); err != nil {
			return err
		}
		if ca := os.Getenv("CA_CERT"); ca != "" {
			return os.WriteFile(caPath, []byte(ca), 0644)
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
	if err := os.WriteFile(certPath, []byte(c.Cert), 0644); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(c.PrivateKey), 0600); err != nil {
		return err
	}
	if c.CACert != "" {
		return os.WriteFile(caPath, []byte(c.CACert), 0644)
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
