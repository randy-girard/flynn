package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cheggaaa/pb"
	cfg "github.com/flynn/flynn/cli/config"
	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"github.com/flynn/flynn/pkg/backup"
	"github.com/flynn/flynn/pkg/shutdown"
	"github.com/flynn/flynn/pkg/term"
	"github.com/flynn/go-docopt"
)

func init() {
	register("cluster", runClusterList, `
usage: flynn cluster

List clusters configured in ~/.flynnrc.
`)
	register("cluster:add", runClusterAdd, `
usage: flynn cluster:add [-f] [-d] [--git-url <giturl>] [--no-git] [--dashboard-url <url>] [--image-url <url>] [--docker-push-url <url>] [--docker] [-p <tlspin>] <cluster-name> <domain> <key>

Add <cluster-name> to the ~/.flynnrc configuration file.

Options:
	-f, --force               force add cluster
	-d, --default             set as default cluster
	--git-url=<giturl>        git URL
	--no-git                  skip git configuration
	--dashboard-url=<url>     public dashboard URL (defaults to https://dashboard.<domain>)
	--image-url=<url>         image URL
	--docker-push-url=<url>   [DEPRECATED] Docker push URL
	--docker                  [DEPRECATED] configure Docker to push to the cluster
	-p, --tls-pin=<tlspin>    SHA256 of the cluster's TLS cert

Examples:

	$ flynn cluster:add -p KGCENkp53YF5OvOKkZIry71+czFRkSw2ZdMszZ/0ljs= default dev.localflynn.com e09dc5301d72be755a3d666f617c4600
	Cluster "default" added.
`)
	register("cluster:remove", runClusterRemove, `
usage: flynn cluster:remove <cluster-name>

Remove <cluster-name> from the ~/.flynnrc configuration file.
`)
	register("cluster:default", runClusterDefault, `
usage: flynn cluster:default [<cluster-name>]

Print the default cluster, or set it to <cluster-name>.
`)
	register("cluster:refresh", runClusterRefresh, `
usage: flynn cluster:refresh [--clear]

Refresh this laptop's cluster entry in ~/.flynnrc to match the live cluster:
TLS pin, CA, git URLs, and controller/git/image/dashboard URLs after a host
domain migration.

Options:
	--clear  Remove the TLS pin entirely instead of updating it.
	         Recommended for Let's Encrypt certificates signed by a trusted CA.

Examples:

	$ flynn cluster:refresh
	Updated TLS pin for cluster "default".

	$ flynn cluster:refresh --clear
	Cleared TLS pin for cluster "default". Standard TLS verification will be used.
`)
	// Hidden for one release: still run after the "moved to flynn-host" hint.
	register("cluster:backup", runClusterBackup, `
usage: flynn cluster:backup [--file <file>]

Takes a backup of the cluster. Moved to flynn-host backup.

Options:
	--file=<backup-file>  file to write backup to (defaults to stdout)
`)
	register("cluster:migrate-domain", runClusterMigrateDomain, `
usage: flynn cluster:migrate-domain <domain>

Migrates the cluster's base domain. Moved to flynn-host migrate-domain.
`)
	register("cluster:log-sink", runLogSink, `
usage: flynn cluster:log-sink
       flynn cluster:log-sink add syslog [--scope <scope>] [--app <app>] [--use-ids] [--insecure] [--format <format>] <url> [<prefix>]
       flynn cluster:log-sink remove <id>

Manage cluster-wide log sinks. Moved to flynn-host log-sink.

Options:
	--scope=<scope>    system (Flynn jobs), apps (user apps), or all [default: all]
	--app=<app>        Limit the sink to one app name or ID
	--use-ids          Use app IDs instead of app names in the syslog APP-NAME field.
	--insecure         Don't verify servers certificate chain or hostname. Should only be used for testing.
	--format=<format>  One of rfc6587, newline, or prefixed_newline. Defaults to rfc6587.
`)
}

func runClusterList(_ *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "NAME", "CONTROLLER URL", "DASHBOARD URL", "GIT URL", "IMAGE URL")
	for _, s := range config.Clusters {
		gitURL := s.GitURL
		if gitURL == "" {
			gitURL = "(none)"
		}
		imageURL := s.ImageURL
		if imageURL == "" {
			imageURL = "(none)"
		}
		dash := s.DashboardURL
		if dash == "" {
			dash = s.OAuthURL
		}
		if dash == "" {
			dash = "(none)"
		}
		data := []interface{}{s.Name, s.ControllerURL, dash, gitURL, imageURL}
		if s.Name == config.Default {
			data = append(data, "(default)")
		}
		listRec(w, data...)
	}
	return nil
}

func runClusterAdd(args *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	s := &cfg.Cluster{
		Name:          args.String["<cluster-name>"],
		Key:           args.String["<key>"],
		GitURL:        args.String["--git-url"],
		ImageURL:      args.String["--image-url"],
		DockerPushURL: args.String["--docker-push-url"],
		TLSPin:        args.String["--tls-pin"],
	}
	dash := strings.TrimSpace(args.String["--dashboard-url"])
	if dash != "" {
		s.DashboardURL = dash
	}
	domain := args.String["<domain>"]

	// handle legacy use where <domain> is the controller URL
	domain = strings.TrimPrefix(domain, "https://controller.")

	s.ControllerURL = "https://controller." + domain
	if s.DashboardURL == "" {
		s.DashboardURL = "https://dashboard." + domain
	}
	if s.GitURL == "" && !args.Bool["--no-git"] {
		s.GitURL = "https://git." + domain
	}
	if s.ImageURL == "" {
		s.ImageURL = "https://images." + domain
	}
	if s.DockerPushURL == "" && args.Bool["--docker"] {
		s.DockerPushURL = "https://docker." + domain
	}

	if err := config.Add(s, args.Bool["--force"]); err != nil {
		return err
	}

	setDefault := args.Bool["--default"] || len(config.Clusters) == 1

	if setDefault && !config.SetDefault(s.Name) {
		return errors.New(fmt.Sprintf("Cluster %q does not exist and cannot be set as default.", s.Name))
	}

	var caPath string
	if s.GitURL != "" || s.DockerPushURL != "" {
		client, err := s.Client()
		if err != nil {
			return err
		}
		caPath, err = writeCACert(client, s.Name)
		if err != nil {
			return fmt.Errorf("Error writing CA certificate: %s", err)
		}
	}

	if s.GitURL != "" {
		if _, err := exec.LookPath("git"); err != nil {
			if serr, ok := err.(*exec.Error); ok && serr.Err == exec.ErrNotFound {
				return errors.New("Executable 'git' was not found. Use --no-git to skip git configuration")
			}
			return err
		}
		if err := cfg.WriteGlobalGitConfig(s.GitURL, caPath); err != nil {
			return err
		}
		cfg.ClearSystemCredentials(s.GitURL)
	}

	if s.DockerPushURL != "" {
		fmt.Fprintln(os.Stderr, "DEPRECATED: Pushing via a Docker registry has been deprecated in favour of pushing via the Flynn image service, set --image-url instead")
		host, err := s.DockerPushHost()
		if err != nil {
			return err
		}
		if err := dockerLogin(host, s.Key); err != nil {
			if e, ok := err.(*exec.Error); ok && e.Err == exec.ErrNotFound {
				err = errors.New("Executable 'docker' was not found.")
			} else if err == ErrDockerTLSError {
				printDockerTLSWarning(host, caPath)
				err = errors.New("Error configuring docker, follow the instructions above then try again")
			}
			return err
		}
	}

	if err := config.SaveTo(configPath()); err != nil {
		return err
	}

	if setDefault {
		log.Printf("Cluster %q added and set as default.", s.Name)
	} else {
		log.Printf("Cluster %q added.", s.Name)
	}
	return nil
}

func writeCACert(c controller.Client, name string) (string, error) {
	data, err := c.GetCACert()
	if err != nil {
		return "", err
	}
	dest, err := cfg.CACertFile(name)
	if err != nil {
		return "", err
	}
	defer dest.Close()
	_, err = dest.Write(data)
	return dest.Name(), err
}

func runClusterRemove(args *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	name := args.String["<cluster-name>"]

	if c := config.Remove(name); c != nil {
		msg := fmt.Sprintf("Cluster %q removed.", name)

		// Select next available cluster as default
		if config.Default == name && len(config.Clusters) > 0 {
			config.SetDefault(config.Clusters[0].Name)
			msg = fmt.Sprintf("Cluster %q removed and %q is now the default cluster.", name, config.Default)
		}

		if err := config.SaveTo(configPath()); err != nil {
			return err
		}

		cfg.RemoveGlobalGitConfig(c.GitURL)
		cfg.ClearSystemCredentials(c.GitURL)

		if host, err := c.DockerPushHost(); err == nil {
			dockerLogout(host)
		}

		log.Print(msg)
	}

	return nil
}

func runClusterDefault(args *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	name := args.String["<cluster-name>"]

	if name == "" {
		w := tabWriter()
		defer w.Flush()
		listRec(w, "NAME", "URL")
		for _, s := range config.Clusters {
			if s.Name == config.Default {
				listRec(w, s.Name, s.ControllerURL, "(default)")
				break
			}
		}
		return nil
	}

	if !config.SetDefault(name) {
		return fmt.Errorf("Cluster %q does not exist and cannot be set as default.", name)
	}
	if err := config.SaveTo(configPath()); err != nil {
		return err
	}

	log.Printf("%q is now the default cluster.", name)
	return nil
}

func runClusterMigrateDomain(args *docopt.Args) error {
	cluster, err := getCluster()
	if err != nil {
		shutdown.Fatal(err)
	}
	client, err := cluster.Client()
	if err != nil {
		shutdown.Fatal(err)
	}

	dm := &ct.DomainMigration{
		Domain: args.String["<domain>"],
	}

	release, err := client.GetAppRelease("controller")
	if err != nil {
		return err
	}
	dm.OldDomain = release.Env["DEFAULT_ROUTE_DOMAIN"]

	if !promptYesNo(fmt.Sprintf("Migrate cluster domain from %q to %q?", dm.OldDomain, dm.Domain)) {
		fmt.Println("Aborted")
		return nil
	}

	maxDuration := 2 * time.Minute
	fmt.Printf("Migrating cluster domain (this can take up to %s)...\n", maxDuration)

	events := make(chan *ct.Event)
	stream, err := client.StreamEvents(ct.StreamEventsOptions{
		ObjectTypes: []ct.EventType{ct.EventTypeDomainMigration},
	}, events)
	if err != nil {
		return nil
	}
	defer stream.Close()

	if err := client.PutDomain(dm); err != nil {
		return err
	}

	timeout := time.After(maxDuration)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return stream.Err()
			}
			var e *ct.DomainMigrationEvent
			if err := json.Unmarshal(event.Data, &e); err != nil {
				return err
			}
			if e.Error != "" {
				fmt.Println(e.Error)
			}
			if e.DomainMigration.FinishedAt != nil {
				dm = e.DomainMigration
				fmt.Printf("Changed cluster domain from %q to %q\n", dm.OldDomain, dm.Domain)

				// update flynnrc
				cluster.TLSPin = dm.TLSCert.Pin
				cluster.ControllerURL = fmt.Sprintf("https://controller.%s", dm.Domain)
				cluster.GitURL = fmt.Sprintf("https://git.%s", dm.Domain)
				cluster.ImageURL = fmt.Sprintf("https://images.%s", dm.Domain)
				cluster.DockerPushURL = fmt.Sprintf("https://docker.%s", dm.Domain)
				if cluster.DashboardURL == fmt.Sprintf("https://dashboard.%s", dm.OldDomain) {
					cluster.DashboardURL = fmt.Sprintf("https://dashboard.%s", dm.Domain)
				}
				if cluster.OAuthURL == fmt.Sprintf("https://dashboard.%s", dm.OldDomain) {
					cluster.OAuthURL = fmt.Sprintf("https://dashboard.%s", dm.Domain)
				}
				if err := config.SaveTo(configPath()); err != nil {
					return fmt.Errorf("Error saving config: %s", err)
				}

				// update git config
				caFile, err := cfg.CACertFile(cluster.Name)
				if err != nil {
					return err
				}
				defer caFile.Close()
				if _, err := caFile.Write([]byte(dm.TLSCert.CACert)); err != nil {
					return err
				}
				if err := cfg.WriteGlobalGitConfig(cluster.GitURL, caFile.Name()); err != nil {
					return err
				}
				cfg.ClearSystemCredentials(cluster.GitURL)
				cfg.RemoveGlobalGitConfig(fmt.Sprintf("https://git.%s", dm.OldDomain))
				cfg.ClearSystemCredentials(fmt.Sprintf("https://git.%s", dm.OldDomain))

				// try to run "docker login" for the new domain, but just print a warning
				// if it fails so the user can fix it later
				if host, err := cluster.DockerPushHost(); err == nil {
					if err := dockerLogin(host, cluster.Key); err == ErrDockerTLSError {
						printDockerTLSWarning(host, caFile.Name())
					}
				}
				dockerLogout(dm.OldDomain)

				fmt.Println("Updated local CLI configuration")
				return nil
			}
		case <-timeout:
			return errors.New("timed out waiting for domain migration to complete")
		}
	}
}

func runClusterRefresh(args *docopt.Args) error {
	if err := readConfig(); err != nil {
		return err
	}
	cluster, err := getCluster()
	if err != nil {
		return err
	}

	if args.Bool["--clear"] {
		if cluster.TLSPin == "" {
			log.Printf("Cluster %q already has no TLS pin configured.", cluster.Name)
			return nil
		}
		cluster.TLSPin = ""
		if err := config.SaveTo(configPath()); err != nil {
			return fmt.Errorf("Error saving config: %s", err)
		}
		log.Printf("Cleared TLS pin for cluster %q. Standard TLS verification will be used.", cluster.Name)
		return nil
	}

	if err := refreshClusterLocalURLs(cluster); err != nil {
		return err
	}
	return updateClusterTLSPin(cluster)
}

func refreshClusterLocalURLs(cluster *cfg.Cluster) error {
	client, err := cluster.Client()
	if err != nil {
		return err
	}
	release, err := client.GetAppRelease("controller")
	if err != nil {
		return err
	}
	domain := release.Env["DEFAULT_ROUTE_DOMAIN"]
	if domain == "" {
		return nil
	}
	oldDomain := controllerDomain(cluster.ControllerURL)
	if oldDomain == "" || oldDomain == domain {
		if err := writeClusterCA(client, cluster); err != nil {
			log.Printf("Warning: could not refresh CA certificate: %s", err)
		}
		return nil
	}

	cluster.ControllerURL = fmt.Sprintf("https://controller.%s", domain)
	cluster.GitURL = fmt.Sprintf("https://git.%s", domain)
	cluster.ImageURL = fmt.Sprintf("https://images.%s", domain)
	cluster.DockerPushURL = fmt.Sprintf("https://docker.%s", domain)
	if cluster.DashboardURL == fmt.Sprintf("https://dashboard.%s", oldDomain) {
		cluster.DashboardURL = fmt.Sprintf("https://dashboard.%s", domain)
	}
	if cluster.OAuthURL == fmt.Sprintf("https://dashboard.%s", oldDomain) {
		cluster.OAuthURL = fmt.Sprintf("https://dashboard.%s", domain)
	}
	if err := config.SaveTo(configPath()); err != nil {
		return fmt.Errorf("Error saving config: %s", err)
	}
	if err := writeClusterCA(client, cluster); err != nil {
		return err
	}
	caFile, err := cfg.CACertFile(cluster.Name)
	if err != nil {
		return err
	}
	defer caFile.Close()
	if err := cfg.WriteGlobalGitConfig(cluster.GitURL, caFile.Name()); err != nil {
		return err
	}
	cfg.ClearSystemCredentials(cluster.GitURL)
	cfg.RemoveGlobalGitConfig(fmt.Sprintf("https://git.%s", oldDomain))
	cfg.ClearSystemCredentials(fmt.Sprintf("https://git.%s", oldDomain))
	log.Printf("Updated local URLs for cluster %q to domain %q.", cluster.Name, domain)
	return nil
}

func controllerDomain(controllerURL string) string {
	u, err := url.Parse(controllerURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	return strings.TrimPrefix(host, "controller.")
}

func writeClusterCA(client controller.Client, cluster *cfg.Cluster) error {
	data, err := client.GetCACert()
	if err != nil {
		return err
	}
	dest, err := cfg.CACertFile(cluster.Name)
	if err != nil {
		return err
	}
	defer dest.Close()
	_, err = dest.Write(data)
	return err
}

func updateClusterTLSPin(cluster *cfg.Cluster) error {
	u, err := url.Parse(cluster.ControllerURL)
	if err != nil {
		return fmt.Errorf("Error parsing controller URL: %s", err)
	}

	host := u.Host
	if !strings.Contains(host, ":") {
		host = host + ":443"
	}

	// Connect without certificate verification to get the current cert
	conn, err := tls.Dial("tcp", host, &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return fmt.Errorf("Error connecting to controller: %s", err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return errors.New("No certificates returned by controller")
	}

	// Calculate the pin (SHA256 of the leaf certificate's DER bytes, base64 encoded)
	leafCert := state.PeerCertificates[0]
	h := sha256.Sum256(leafCert.Raw)
	newPin := base64.StdEncoding.EncodeToString(h[:])

	if cluster.TLSPin == newPin {
		log.Printf("TLS pin for cluster %q is already up to date.", cluster.Name)
		return nil
	}

	oldPin := cluster.TLSPin
	cluster.TLSPin = newPin
	if err := config.SaveTo(configPath()); err != nil {
		return fmt.Errorf("Error saving config: %s", err)
	}

	if oldPin == "" {
		log.Printf("Set TLS pin for cluster %q.", cluster.Name)
	} else {
		log.Printf("Updated TLS pin for cluster %q.", cluster.Name)
	}

	// Show certificate info for verification
	fmt.Printf("Certificate Subject: %s\n", leafCert.Subject.CommonName)
	fmt.Printf("Certificate Issuer: %s\n", leafCert.Issuer.CommonName)
	fmt.Printf("Valid Until: %s\n", leafCert.NotAfter.Format(time.RFC3339))
	fmt.Printf("New Pin: %s\n", newPin)

	return nil
}

func runClusterBackup(args *docopt.Args) error {
	client, err := getClusterClient()
	if err != nil {
		return err
	}

	var bar *pb.ProgressBar
	var progress backup.ProgressBar
	if term.IsTerminal(os.Stderr.Fd()) {
		bar = pb.New(0)
		bar.SetUnits(pb.U_BYTES)
		bar.ShowBar = false
		bar.ShowSpeed = true
		bar.Output = os.Stderr
		bar.Start()
		progress = bar
	}

	var dest io.Writer = os.Stdout
	if filename := args.String["--file"]; filename != "" {
		f, err := os.Create(filename)
		if err != nil {
			return err
		}
		defer f.Close()
		dest = f
	}

	fmt.Fprintln(os.Stderr, "Creating cluster backup...")

	if err := backup.Run(client, dest, progress); err != nil {
		return err
	}
	if bar != nil {
		bar.Finish()
	}
	fmt.Fprintln(os.Stderr, "Backup complete.")

	return nil
}

func runLogSink(args *docopt.Args) error {
	client, err := getClusterClient()
	if err != nil {
		return err
	}

	if args.Bool["add"] {
		switch {
		case args.Bool["syslog"]:
			return runLogSinkAddSyslog(args, client)
		default:
			return fmt.Errorf("Sink kind not supported")
		}
	}
	if args.Bool["remove"] {
		return runLogSinkRemove(args, client)
	}

	sinks, err := client.ListSinks()
	if err != nil {
		return err
	}

	w := tabWriter()
	defer w.Flush()

	listRec(w, "ID", "KIND", "CONFIG")
	for _, j := range sinks {
		var config string
		if j.Config != nil {
			config = string(*j.Config)
		}
		listRec(w, j.ID, j.Kind, config)
	}

	return nil
}

func runLogSinkAddSyslog(args *docopt.Args, client controller.Client) error {
	u, err := url.Parse(args.String["<url>"])
	if err != nil {
		return fmt.Errorf("Invalid syslog URL: %s", err)
	}
	switch u.Scheme {
	case "syslog", "syslog+tls":
	default:
		return fmt.Errorf("Invalid syslog protocol: %s", u.Scheme)
	}

	var format ct.SyslogFormat
	switch args.String["--format"] {
	case "newline":
		format = ct.SyslogFormatNewline
	case "prefixed_newline":
		format = ct.SyslogFormatPrefixedNewline
	case "rfc6587", "":
		format = ct.SyslogFormatRFC6587
	default:
		return fmt.Errorf("Invalid syslog format: %s", args.String["--format"])
	}

	scope, err := ct.ParseSinkScope(args.String["--scope"])
	if err != nil {
		return err
	}
	appID := ""
	if name := args.String["--app"]; name != "" {
		app, err := client.GetApp(name)
		if err != nil {
			return fmt.Errorf("app %q: %w", name, err)
		}
		appID = app.ID
	}

	data, _ := json.Marshal(ct.SyslogSinkConfig{
		Prefix:   args.String["<prefix>"],
		URL:      u.String(),
		UseIDs:   args.Bool["--use-ids"],
		Insecure: args.Bool["--insecure"],
		Format:   format,
		Scope:    scope,
	})
	config := json.RawMessage(data)

	sink := &ct.Sink{
		Kind:   ct.SinkKindSyslog,
		AppID:  appID,
		Config: &config,
	}

	if err := client.CreateSink(sink); err != nil {
		return err
	}

	log.Printf("Created sink %s.", sink.ID)

	return nil
}

func runLogSinkRemove(args *docopt.Args, client controller.Client) error {
	id := args.String["<id>"]

	res, err := client.DeleteSink(id)
	if err != nil {
		return err
	}

	log.Printf("Deleted sink %s.", res.ID)

	return nil
}
