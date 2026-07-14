package kafka

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flynn/flynn/pkg/certgen"
)

// TLSBundle holds the PEM encoded material for a Kafka cluster's mutual TLS
// setup. A single private CA signs the broker (server) certificate and the
// client certificate handed to consuming apps.
type TLSBundle struct {
	CACert     string `json:"ca_cert"`
	ServerCert string `json:"server_cert"`
	ServerKey  string `json:"server_key"`
	ClientCert string `json:"client_cert"`
	ClientKey  string `json:"client_key"`
}

// GenerateTLSBundle creates a fresh private CA and issues a server certificate
// (covering hosts) and a client certificate for mutual TLS.
func GenerateTLSBundle(hosts []string) (*TLSBundle, error) {
	ca, err := certgen.Generate(certgen.Params{IsCA: true})
	if err != nil {
		return nil, fmt.Errorf("generate ca: %s", err)
	}
	server, err := certgen.Generate(certgen.Params{Hosts: hosts, CA: ca})
	if err != nil {
		return nil, fmt.Errorf("generate server cert: %s", err)
	}
	client, err := certgen.Generate(certgen.Params{Hosts: []string{"flynn-kafka-client"}, Client: true, CA: ca})
	if err != nil {
		return nil, fmt.Errorf("generate client cert: %s", err)
	}
	return &TLSBundle{
		CACert:     ca.PEM,
		ServerCert: server.PEM,
		ServerKey:  server.KeyPEM,
		ClientCert: client.PEM,
		ClientKey:  client.KeyPEM,
	}, nil
}

// WriteServerStores materializes a PKCS12 keystore (broker identity) and
// truststore (the CA) in dir and returns their paths. openssl and keytool must
// be available on PATH.
func WriteServerStores(dir, caCert, serverCert, serverKey, password string) (keystore, truststore string, err error) {
	keystore = filepath.Join(dir, "keystore.p12")
	truststore = filepath.Join(dir, "truststore.p12")

	if err := buildKeystore(dir, keystore, caCert, serverCert, serverKey, password, "broker"); err != nil {
		return "", "", err
	}
	if err := buildTruststore(dir, truststore, caCert, password); err != nil {
		return "", "", err
	}
	return keystore, truststore, nil
}

// WriteClientConfig materializes a client keystore/truststore and a
// client.properties file suitable for the kafka-*.sh admin tools'
// --command-config flag. It returns the path to the properties file.
func WriteClientConfig(dir, caCert, clientCert, clientKey, password string) (string, error) {
	keystore := filepath.Join(dir, "client-keystore.p12")
	truststore := filepath.Join(dir, "client-truststore.p12")
	propsPath := filepath.Join(dir, "client.properties")

	if err := buildKeystore(dir, keystore, caCert, clientCert, clientKey, password, "client"); err != nil {
		return "", err
	}
	if err := buildTruststore(dir, truststore, caCert, password); err != nil {
		return "", err
	}

	props := clientProperties(keystore, truststore, password)
	if err := os.WriteFile(propsPath, []byte(props), 0600); err != nil {
		return "", err
	}
	return propsPath, nil
}

// ClientProperties returns the SSL client configuration contents. Hostname
// identification is disabled because brokers advertise dynamic per-node IPs
// while all certificates are validated against the shared private CA.
func clientProperties(keystore, truststore, password string) string {
	return fmt.Sprintf(`security.protocol=SSL
ssl.truststore.location=%s
ssl.truststore.password=%s
ssl.truststore.type=PKCS12
ssl.keystore.location=%s
ssl.keystore.password=%s
ssl.keystore.type=PKCS12
ssl.endpoint.identification.algorithm=
`, truststore, password, keystore, password)
}

// buildKeystore converts a PEM cert/key pair into a PKCS12 keystore via openssl.
func buildKeystore(dir, out, caCert, cert, key, password, alias string) error {
	certFile := filepath.Join(dir, alias+".pem")
	keyFile := filepath.Join(dir, alias+".key")
	caFile := filepath.Join(dir, alias+"-ca.pem")

	for path, contents := range map[string]string{certFile: cert, keyFile: key, caFile: caCert} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			return err
		}
	}

	cmd := exec.Command("openssl", "pkcs12", "-export",
		"-in", certFile,
		"-inkey", keyFile,
		"-certfile", caFile,
		"-name", alias,
		"-out", out,
		"-passout", "pass:"+password,
	)
	if outB, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("openssl pkcs12: %s: %s", err, outB)
	}
	return nil
}

// buildTruststore imports the CA certificate into a PKCS12 truststore via keytool.
func buildTruststore(dir, out, caCert, password string) error {
	caFile := filepath.Join(dir, "truststore-ca.pem")
	if err := os.WriteFile(caFile, []byte(caCert), 0600); err != nil {
		return err
	}
	// keytool refuses to import into an existing store; start clean.
	os.Remove(out)

	cmd := exec.Command("keytool", "-importcert",
		"-noprompt",
		"-alias", "ca",
		"-file", caFile,
		"-keystore", out,
		"-storetype", "PKCS12",
		"-storepass", password,
	)
	if outB, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keytool importcert: %s: %s", err, outB)
	}
	return nil
}
