package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	flanneldisc "github.com/randy-girard/flynn/flannel/discoverd"
	"github.com/randy-girard/flynn/flannel/subnet"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/term"
	router "github.com/randy-girard/flynn/router/types"
)

// errProductionConfigureStopped is returned when the operator interrupts the
// wizard. Commands that already ran stay applied.
var errProductionConfigureStopped = errors.New("configure stopped")

const productionUsage = `
usage: flynn-host production

List check and configure. This does not start the wizard.
`

const productionConfigureUsage = `
usage: flynn-host production:configure [--zpool-name=<name>]

Walk production settings for a self-hosted or hosted cluster. It is safe to
re-run. The cluster is the saved progress. There is no progress file.

Options:
	--zpool-name=<name>  pool passed to zpool status -P [default: flynn-default]
`

func init() {
	Register("production", runProduction, productionUsage)
	Register("production:configure", runProductionConfigureCmd, productionConfigureUsage)
}

func runProduction(_ *docopt.Args) error {
	fmt.Print(FormatHelp("production"))
	return nil
}

// productionConfigureState is the cluster snapshot the wizard reads. Tests
// fill it. The live command loads it from the controller and this host.
type productionConfigureState struct {
	Check     productionCheckConfig
	ACME      *ct.ACMEConfig
	ACMEErr   error
	Apps      []*ct.App
	AppsErr   error
	Routes    []*router.Route
	RoutesErr error
	Domain    string
}

// productionAsker reads one line. A stop error aborts the wizard.
type productionAsker interface {
	Line(question string) (string, error)
}

type stdinAsker struct {
	in *bufio.Reader
}

func newStdinAsker() stdinAsker {
	return stdinAsker{in: bufio.NewReader(os.Stdin)}
}

func (s stdinAsker) Line(string) (string, error) {
	line, err := s.in.ReadString('\n')
	line = strings.TrimSpace(line)
	if err == io.EOF && line == "" {
		return "", errProductionConfigureStopped
	}
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

func runProductionConfigureCmd(args *docopt.Args, client *cluster.Client) error {
	pool := strings.TrimSpace(args.String["--zpool-name"])
	state := loadProductionConfigureState(client, pool)
	state.Check.Color = productionColorEnabled(os.Stdout)
	return runProductionConfigure(os.Stdout, state, newStdinAsker(), productionDispatch, term.IsTerminal(os.Stdin.Fd()))
}

func loadProductionConfigureState(client *cluster.Client, pool string) productionConfigureState {
	state := productionConfigureState{Check: hostProductionCheckConfig(client, pool)}
	ctrl, err := controllerClient()
	if err != nil {
		state.ACMEErr = err
		state.AppsErr = err
		state.RoutesErr = err
		return state
	}
	state.ACME, state.ACMEErr = ctrl.GetACMEConfig()
	state.Apps, state.AppsErr = ctrl.AppList()
	state.Routes, state.RoutesErr = ctrl.RouteList()
	if rel, err := ctrl.GetAppRelease("controller"); err == nil && rel != nil {
		state.Domain = rel.Env["DEFAULT_ROUTE_DOMAIN"]
	}
	return state
}

func productionDispatch(argv []string) error {
	if len(argv) == 0 {
		return errors.New("empty production command")
	}
	return Run(argv[0], argv[1:])
}

type configureWizard struct {
	out   io.Writer
	ask   productionAsker
	run   func([]string) error
	tty   bool
	state productionConfigureState
	mode  string
	// configuredThisRun is set when letsencrypt:configure is invoked.
	configuredThisRun bool
	installedThisRun  bool
}

func runProductionConfigure(out io.Writer, state productionConfigureState, ask productionAsker, run func([]string) error, tty bool) error {
	w := &configureWizard{out: out, ask: ask, run: run, tty: tty, state: state}
	steps := []func() error{
		w.tenancy,
		w.blobstore,
		w.zfs,
		w.hostCap,
		w.reserve,
		w.logSink,
		w.tls,
		w.backup,
		w.overlay,
		w.dns,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	w.heading("production:check")
	fmt.Fprintln(w.out, productionCheckSentence(w.mode))
	return runProductionChecks(w.out, w.state.Check)
}

func (w *configureWizard) heading(name string) {
	fmt.Fprintf(w.out, "== %s\n", name)
}

func (w *configureWizard) line(question string) (string, error) {
	fmt.Fprintln(w.out, question)
	if !w.tty {
		fmt.Fprintln(w.out, "no TTY; not applying")
		return "", nil
	}
	if w.ask == nil {
		return "", errProductionConfigureStopped
	}
	return w.ask.Line(question)
}

func (w *configureWizard) confirm(question string) (bool, error) {
	answer, err := w.line(question)
	if err != nil {
		return false, err
	}
	return isYes(answer), nil
}

func isYes(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func (w *configureWizard) exec(argv []string) error {
	if w.run == nil {
		return errors.New("no command runner")
	}
	return w.run(append([]string(nil), argv...))
}

func productionCheckSentence(mode string) string {
	switch mode {
	case tenancy.ModeHosted:
		return "production:check in hosted mode fails Postgres, a file-backed pool, a missing host cap, and a missing syslog+tls sink. The reserve line stays ok because hosted placement reserves small, medium, and large even when reserve_resources is off."
	case tenancy.ModeSelfHosted:
		return "production:check in self_hosted mode warns on Postgres and a missing syslog+tls sink. A warn can still be a finished wizard step. The reserve line stays ok. Self-hosted placement follows the stored reserve_resources flag. A file-backed pool and a missing host cap still fail."
	default:
		return "production:check fails when tenancy cannot be read. Unknown mode is not self-hosted."
	}
}

func (w *configureWizard) tenancy() error {
	w.heading("Tenancy")
	fmt.Fprintln(w.out, "self_hosted is a company running Flynn for itself. hosted is a cluster whose customers share it.")
	fmt.Fprintln(w.out, "tenancy:mode writes only cluster_settings.tenancy_mode. It does not enable signup, does not change reserve_resources, and does not run production:check. hosted is refused when production:check would fail.")
	mode, err := w.readTenancy()
	if err != nil {
		fmt.Fprintf(w.out, "Current mode: error=%s\n", err)
		fmt.Fprintln(w.out, "Not running tenancy:mode.")
		return nil
	}
	w.mode = mode
	fmt.Fprintf(w.out, "Current mode: %s\n", mode)
	change, err := w.confirm("Change tenancy mode?")
	if err != nil {
		return err
	}
	if !change {
		fmt.Fprintln(w.out, "Not running tenancy:mode.")
		return nil
	}
	next, err := w.line("Tenancy mode (self_hosted or hosted):")
	if err != nil {
		return err
	}
	next = strings.TrimSpace(next)
	if next != tenancy.ModeSelfHosted && next != tenancy.ModeHosted {
		fmt.Fprintln(w.out, "Not running tenancy:mode.")
		return nil
	}
	if next == mode {
		fmt.Fprintln(w.out, "Already matches. Not running tenancy:mode.")
		return nil
	}
	if err := w.exec([]string{"tenancy:mode", next}); err != nil {
		return err
	}
	w.mode = next
	return nil
}

func (w *configureWizard) readTenancy() (string, error) {
	if w.state.Check.API == nil {
		return "", errors.New("no controller client")
	}
	var settings ct.TenancySettings
	if err := w.state.Check.API.Get("/tenancy", &settings); err != nil {
		return "", err
	}
	return settings.Mode, nil
}

func (w *configureWizard) blobstore() error {
	w.heading("Blobstore")
	fmt.Fprintln(w.out, "A new default is not proof the old copies are gone. A later --delete does not retry rows SetBackend already moved.")
	fmt.Fprintln(w.out, "This does not scan files or pg_largeobject.")
	name, kind, postgres, err := w.readBlobstore()
	if err != nil {
		fmt.Fprintf(w.out, "Current blobstore: error=%s\n", err)
		fmt.Fprintln(w.out, "Not running blobstore:set.")
		return nil
	}
	fmt.Fprintf(w.out, "Current default=%s type=%s.\n", name, kind)
	if !postgres {
		fmt.Fprintln(w.out, "An external default is done in either mode. Not running blobstore:set. Not asking for keys.")
		return nil
	}
	fmt.Fprintln(w.out, "Postgres is not done in either mode. Hosted production:check fails this line. Self-hosted warns.")
	choice, err := w.line("Blobstore backend (s3, minio, gcs, azure, or skip):")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(choice)) {
	case "s3":
		return w.blobstoreS3("s3")
	case "minio":
		return w.blobstoreMinIO()
	case "gcs":
		return w.blobstoreEnvMigrate("gcs")
	case "azure":
		return w.blobstoreEnvMigrate("azure")
	default:
		fmt.Fprintln(w.out, "Not running blobstore:set.")
		return nil
	}
}

func (w *configureWizard) readBlobstore() (name, kind string, postgres bool, err error) {
	if w.state.Check.API == nil {
		return "", "", true, errors.New("no controller client")
	}
	release, err := w.state.Check.API.GetAppRelease("blobstore")
	if err != nil {
		return "", "", true, err
	}
	if release == nil {
		return "", "", true, errors.New("blobstore release is empty")
	}
	var backends map[string]map[string]string
	name, backends, err = data.BackendsFromEnv(data.EnvMapToSlice(release.Env))
	if err != nil {
		return "", "", true, err
	}
	postgres = blobstoreDefaultIsPostgres(name, backends)
	if info := backends[strings.ToLower(name)]; info != nil {
		kind = info["backend"]
	}
	if name == "" || strings.EqualFold(name, "postgres") {
		name = "postgres"
	}
	if kind == "" {
		kind = "postgres"
	}
	return name, kind, postgres, nil
}

func (w *configureWizard) blobstoreS3(backend string) error {
	bucket, err := w.line("Bucket:")
	if err != nil {
		return err
	}
	region, err := w.line("S3 region (us-east-1):")
	if err != nil {
		return err
	}
	key, err := w.line("Access key id:")
	if err != nil {
		return err
	}
	secret, err := w.line("Secret access key:")
	if err != nil {
		return err
	}
	endpoint, err := w.line("Endpoint (empty for AWS):")
	if err != nil {
		return err
	}
	return w.confirmBlobstoreSet(backend, bucket, region, endpoint, key, secret, false)
}

func (w *configureWizard) blobstoreMinIO() error {
	bucket, err := w.line("Bucket:")
	if err != nil {
		return err
	}
	endpoint, err := w.line("Endpoint:")
	if err != nil {
		return err
	}
	insecure, err := w.confirm("Insecure HTTP?")
	if err != nil {
		return err
	}
	key, err := w.line("Access key id:")
	if err != nil {
		return err
	}
	secret, err := w.line("Secret access key:")
	if err != nil {
		return err
	}
	region, err := w.line("MinIO location (us-east-1):")
	if err != nil {
		return err
	}
	return w.confirmBlobstoreSet("minio", bucket, region, endpoint, key, secret, insecure)
}

func (w *configureWizard) confirmBlobstoreSet(backend, bucket, region, endpoint, key, secret string, insecure bool) error {
	bucket = strings.TrimSpace(bucket)
	region = strings.TrimSpace(region)
	endpoint = strings.TrimSpace(endpoint)
	key = strings.TrimSpace(key)
	secret = strings.TrimSpace(secret)
	if region == "" {
		region = "us-east-1"
	}
	fmt.Fprintln(w.out, "blobstore:set --region is the S3 or MinIO location. It is not written to cluster_settings.region.")
	if bucket == "" {
		fmt.Fprintln(w.out, "Not running blobstore:set.")
		return nil
	}
	argv := []string{"blobstore:set", "--backend=" + backend, "--bucket=" + bucket, "--region=" + region}
	if endpoint != "" {
		argv = append(argv, "--endpoint="+endpoint)
	}
	if insecure {
		argv = append(argv, "--insecure")
	}
	if key != "" {
		argv = append(argv, "--access-key-id="+key)
	}
	argv = append(argv, "--secret-access-key="+secret, "--migrate", "--delete")
	printed := make([]string, len(argv))
	copy(printed, argv)
	for i, arg := range printed {
		if strings.HasPrefix(arg, "--secret-access-key=") {
			printed[i] = "--secret-access-key=<redacted>"
		}
	}
	fmt.Fprintf(w.out, "Command: flynn-host %s\n", strings.Join(printed, " "))
	ok, err := w.confirm("Run blobstore:set --migrate --delete?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w.out, "Not running blobstore:set.")
		return nil
	}
	return w.exec(argv)
}

func (w *configureWizard) blobstoreEnvMigrate(kind string) error {
	switch kind {
	case "azure":
		fmt.Fprintln(w.out, `flynn -a blobstore env:set BACKEND_AZUREMAIN="backend=azure account_name=<account> account_key=<key> container=<container>"`)
		fmt.Fprintln(w.out, "flynn -a blobstore env:set DEFAULT_BACKEND=azuremain")
	default:
		fmt.Fprintln(w.out, `flynn -a blobstore env:set BACKEND_GCSMAIN="backend=gcs bucket=<bucket>" BACKEND_GCSMAIN_KEY="<service-account-json>"`)
		fmt.Fprintln(w.out, "flynn -a blobstore env:set DEFAULT_BACKEND=gcsmain")
	}
	fmt.Fprintln(w.out, "That env:set path is not blobstore:set. Set it before migrating. This wizard does not print secret keys.")
	ok, err := w.confirm("Run blobstore:migrate --delete?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w.out, "Not running blobstore:migrate.")
		return nil
	}
	return w.exec([]string{"blobstore:migrate", "--delete"})
}

func (w *configureWizard) zfs() error {
	w.heading("ZFS")
	fmt.Fprintln(w.out, "ZFS is this host only. Run the wizard on each host. This step does not run zpool attach, zpool detach, or create a pool.")
	pool := strings.TrimSpace(w.state.Check.Pool)
	if pool == "" {
		pool = "flynn-default"
	}
	if !zpoolNamePattern.MatchString(pool) {
		fmt.Fprintf(w.out, "pool=%s error=invalid zpool name. zpool was not run.\n", pool)
		return nil
	}
	if w.state.Check.ZpoolStatus == nil {
		fmt.Fprintln(w.out, "pool status is unavailable. This wizard does not create a pool.")
		return nil
	}
	out, zerr := w.state.Check.ZpoolStatus(pool)
	if zerr != nil {
		fmt.Fprintf(w.out, "pool=%s is missing or zpool status failed: %s\n", pool, zerr)
		fmt.Fprintln(w.out, "This wizard does not create a pool. Run the wizard on each host.")
		return nil
	}
	_, leaves, err := parseZpoolDevices(string(out))
	if err != nil {
		fmt.Fprintf(w.out, "pool=%s error=%s\n", pool, err)
		fmt.Fprintln(w.out, "This wizard does not create a pool. Run the wizard on each host.")
		return nil
	}
	var files []string
	for _, leaf := range leaves {
		if w.state.Check.Stat == nil {
			fmt.Fprintf(w.out, "pool=%s error=stat %s: no stat\n", pool, leaf)
			return nil
		}
		fi, err := w.state.Check.Stat(leaf)
		if err != nil {
			fmt.Fprintf(w.out, "pool=%s error=stat %s: %s\n", pool, leaf, err)
			return nil
		}
		if vdevKind(fi) == "file" {
			files = append(files, leaf)
		}
	}
	if len(files) == 0 {
		fmt.Fprintf(w.out, "pool=%s every leaf is a block device. This host is done. Run the wizard on each host.\n", pool)
		return nil
	}
	sort.Strings(files)
	fmt.Fprintf(w.out, "pool=%s still has a regular-file leaf. That is not done in either mode.\n", pool)
	fmt.Fprintln(w.out, "Mirror the file onto a disk, wait until the resilver finishes, then detach only the file. This wizard does not run these commands:")
	for _, file := range files {
		fmt.Fprintf(w.out, "sudo zpool attach %s %s DISK\n", pool, file)
		fmt.Fprintln(w.out, "# wait until scan: resilvered")
		fmt.Fprintf(w.out, "sudo zpool detach %s %s\n", pool, file)
		fmt.Fprintf(w.out, "sudo rm %s\n", file)
	}
	fmt.Fprintln(w.out, "Run the wizard on each host.")
	return nil
}

func (w *configureWizard) hostCap() error {
	w.heading("Host cap")
	fmt.Fprintln(w.out, "A numeric flynn-max-processes is done. unlimited, 0, empty, off, none, and auto are not a cap. The number is not invented. tags:max-processes --auto is not used.")
	if w.state.Check.Hosts == nil {
		fmt.Fprintln(w.out, "host list: no host source")
		return nil
	}
	hosts, err := w.state.Check.Hosts()
	if err != nil {
		fmt.Fprintf(w.out, "host list: %s\n", err)
		return nil
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i] == nil {
			return false
		}
		if hosts[j] == nil {
			return true
		}
		return hosts[i].ID() < hosts[j].ID()
	})
	for _, h := range hosts {
		if h == nil {
			continue
		}
		label, ok := hostCapLabel(h.Tags())
		fmt.Fprintf(w.out, "%s=%s\n", h.ID(), label)
		if ok {
			fmt.Fprintf(w.out, "%s already has a numeric flynn-max-processes. Not running tags:max-processes.\n", h.ID())
			continue
		}
		answer, err := w.line(fmt.Sprintf("Process cap for %s:", h.ID()))
		if err != nil {
			return err
		}
		n, good := parseProcessCap(answer)
		if !good {
			fmt.Fprintf(w.out, "Not running tags:max-processes for %s.\n", h.ID())
			continue
		}
		if err := w.exec([]string{"tags:max-processes", h.ID(), strconv.Itoa(n)}); err != nil {
			return err
		}
	}
	return nil
}

func parseProcessCap(raw string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unlimited", "off", "none", "auto", "0":
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func (w *configureWizard) reserve() error {
	w.heading("Runtime reservation")
	fmt.Fprintln(w.out, "Hosted placement reserves builtin small, medium, and large even when reserve_resources is false. runtime:reserve only stores the flag.")
	fmt.Fprintln(w.out, "Self-hosted placement follows the stored flag.")
	fmt.Fprintln(w.out, "A readable profile list does not fail the production:check reserve line in either mode.")
	fmt.Fprintln(w.out, "Placement of a reserving process needs a host with that much free CPU and memory. A process that does not fit is blocked.")
	flags, err := w.reserveFlags()
	if err != nil {
		fmt.Fprintf(w.out, "profiles: %s\n", err)
		fmt.Fprintln(w.out, "Not running runtime:reserve.")
		return nil
	}
	fmt.Fprintf(w.out, "small=%t medium=%t large=%t\n", flags["small"], flags["medium"], flags["large"])
	var off []string
	for _, name := range []string{"small", "medium", "large"} {
		on, ok := flags[name]
		if !ok {
			fmt.Fprintf(w.out, "%s is not in the profile list. Not running runtime:reserve for it.\n", name)
			continue
		}
		if !on {
			off = append(off, name)
		}
	}
	if len(off) == 0 {
		fmt.Fprintln(w.out, "Already reserving. Not running runtime:reserve.")
		return nil
	}
	ok, err := w.confirm("Reserve small, medium, and large?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w.out, "Not running runtime:reserve.")
		return nil
	}
	for _, name := range off {
		if err := w.exec([]string{"runtime:reserve", name}); err != nil {
			return err
		}
	}
	return nil
}

func (w *configureWizard) reserveFlags() (map[string]bool, error) {
	if w.state.Check.API == nil {
		return nil, errors.New("no controller client")
	}
	profiles, err := w.state.Check.API.ListRuntimeProfiles()
	if err != nil {
		return nil, err
	}
	flags := map[string]bool{}
	present := map[string]bool{}
	for _, p := range profiles {
		if p == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" {
			continue
		}
		present[name] = true
		flags[name] = p.ReserveResources
	}
	out := map[string]bool{}
	for _, name := range []string{"small", "medium", "large"} {
		if present[name] {
			out[name] = flags[name]
		}
	}
	// Signal missing profiles by absence. Callers use the comma-ok form.
	return out, nil
}

func (w *configureWizard) logSink() error {
	w.heading("Log sink")
	fmt.Fprintln(w.out, "A cluster syslog sink has kind syslog and an empty app id. A sink limited to one app does not count. logaggregator does not count.")
	fmt.Fprintln(w.out, "production:check still counts only syslog+tls, format rfc6587, and no prefix. A syslog:// cluster sink can finish this step while self-hosted warns.")
	sinks, err := w.readSinks()
	if err != nil {
		fmt.Fprintf(w.out, "sinks: %s\n", err)
		fmt.Fprintln(w.out, "Not running log-sink:add.")
		return nil
	}
	var cluster []string
	for _, sink := range sinks {
		raw, ok := clusterSyslogURL(sink)
		if !ok {
			continue
		}
		cluster = append(cluster, raw)
		fmt.Fprintf(w.out, "cluster sink %s url=%s\n", sink.ID, raw)
	}
	if len(cluster) > 0 {
		fmt.Fprintln(w.out, "A cluster syslog sink is already set. Not running log-sink:add.")
		return nil
	}
	raw, err := w.line("Syslog URL:")
	if err != nil {
		return err
	}
	raw = strings.TrimSpace(raw)
	u, perr := url.Parse(raw)
	if perr != nil || (u.Scheme != "syslog" && u.Scheme != "syslog+tls") || u.Host == "" {
		fmt.Fprintln(w.out, "URL is not syslog or syslog+tls. Not running log-sink:add.")
		return nil
	}
	for _, have := range cluster {
		if have == raw {
			fmt.Fprintln(w.out, "That URL is already a cluster sink. Not running log-sink:add.")
			return nil
		}
	}
	return w.exec([]string{"log-sink:add", "syslog", raw})
}

func (w *configureWizard) readSinks() ([]*ct.Sink, error) {
	if w.state.Check.API == nil {
		return nil, errors.New("no controller client")
	}
	var sinks []*ct.Sink
	if err := w.state.Check.API.Get("/sinks", &sinks); err != nil {
		return nil, err
	}
	return sinks, nil
}

func clusterSyslogURL(sink *ct.Sink) (string, bool) {
	if sink == nil || sink.Kind != ct.SinkKindSyslog || strings.TrimSpace(sink.AppID) != "" || sink.Config == nil {
		return "", false
	}
	var cfg ct.SyslogSinkConfig
	if err := json.Unmarshal(*sink.Config, &cfg); err != nil {
		return "", false
	}
	return strings.TrimSpace(cfg.URL), true
}

func (w *configureWizard) tls() error {
	w.heading("TLS")
	cfg := w.state.ACME
	if w.state.ACMEErr != nil {
		fmt.Fprintf(w.out, "letsencrypt:status error=%s\n", w.state.ACMEErr)
	} else if cfg == nil {
		fmt.Fprintln(w.out, "letsencrypt:status enabled=false email=(not configured) terms=false account_key=false")
	} else {
		fmt.Fprintf(w.out, "letsencrypt:status enabled=%t email=%s terms=%t account_key=%t\n", cfg.Enabled, valueOrNone(cfg.ContactEmail), cfg.TermsOfServiceAgreed, cfg.HasAccountKey || strings.TrimSpace(cfg.AccountKey) != "")
	}
	configured := acmeConfigured(cfg)
	if configured {
		fmt.Fprintln(w.out, "Already configured. Not running plugin:install or letsencrypt:configure.")
	} else {
		installed := w.installedThisRun
		if !installed && w.state.AppsErr == nil {
			_, err := lookupLetsEncryptPlugin(w.state.Apps)
			installed = err == nil
		}
		if !installed {
			ok, err := w.confirm("Install the letsencrypt plugin?")
			if err != nil {
				return err
			}
			if ok {
				if err := w.exec([]string{"plugin:install", "letsencrypt"}); err != nil {
					return err
				}
				w.installedThisRun = true
				installed = true
			}
		}
		if installed {
			email, err := w.line("Let's Encrypt email:")
			if err != nil {
				return err
			}
			email = strings.TrimSpace(email)
			staging, err := w.confirm("Use the staging directory?")
			if err != nil {
				return err
			}
			ok, err := w.confirm("Run letsencrypt:configure --agree-tos?")
			if err != nil {
				return err
			}
			if ok && strings.Contains(email, "@") && !strings.Contains(email, " ") {
				argv := []string{"letsencrypt:configure", "--email=" + email, "--agree-tos"}
				if staging {
					argv = append(argv, "--staging")
				}
				if err := w.exec(argv); err != nil {
					return err
				}
				w.configuredThisRun = true
				configured = true
			} else if ok {
				fmt.Fprintln(w.out, "Not running letsencrypt:configure.")
			}
		} else {
			fmt.Fprintln(w.out, "Not running letsencrypt:configure.")
		}
	}
	enabled := w.configuredThisRun || (cfg != nil && cfg.Enabled)
	if !configured || !enabled {
		fmt.Fprintln(w.out, "System routes stay off until ACME is configured and enabled. Not running letsencrypt:enable-system-routes.")
		return nil
	}
	if w.state.RoutesErr != nil || w.state.AppsErr != nil {
		fmt.Fprintln(w.out, "Could not read system routes. Not running letsencrypt:enable-system-routes.")
		return nil
	}
	total, off := systemHTTPRouteCounts(w.state.Apps, w.state.Routes, w.state.Domain)
	fmt.Fprintf(w.out, "system http routes=%d off=%d\n", total, off)
	if total == 0 {
		fmt.Fprintln(w.out, "No system HTTP routes. Not running letsencrypt:enable-system-routes.")
		return nil
	}
	if off == 0 {
		fmt.Fprintln(w.out, "System routes are already on. Not running letsencrypt:enable-system-routes.")
		return nil
	}
	ok, err := w.confirm("Enable Let's Encrypt on system routes?")
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(w.out, "Not running letsencrypt:enable-system-routes.")
		return nil
	}
	return w.exec([]string{"letsencrypt:enable-system-routes"})
}

func acmeConfigured(cfg *ct.ACMEConfig) bool {
	if cfg == nil {
		return false
	}
	return strings.TrimSpace(cfg.ContactEmail) != "" && cfg.TermsOfServiceAgreed && (cfg.HasAccountKey || strings.TrimSpace(cfg.AccountKey) != "")
}

func systemHTTPRouteCounts(apps []*ct.App, routes []*router.Route, domain string) (total, off int) {
	byID := map[string]*ct.App{}
	for _, app := range apps {
		if app != nil && app.ID != "" {
			byID[app.ID] = app
		}
	}
	for _, route := range routes {
		if route == nil || route.Type != "http" || !strings.HasPrefix(route.ParentRef, ct.RouteParentRefPrefix) {
			continue
		}
		appID := strings.TrimPrefix(route.ParentRef, ct.RouteParentRefPrefix)
		app := byID[appID]
		isSystem := app != nil && app.System()
		isBase := domain != "" && route.Domain == domain
		if !isSystem && !isBase {
			continue
		}
		total++
		if route.ManagedCertificateDomain == nil || strings.TrimSpace(*route.ManagedCertificateDomain) == "" {
			off++
		}
	}
	return total, off
}

func (w *configureWizard) backup() error {
	w.heading("Backup")
	fmt.Fprintln(w.out, "flynn-host backup restores the platform and does not include tenant volumes.")
	fmt.Fprintln(w.out, "A later run may write a new file. This step does not build the tenant backup job.")
	path, err := w.line("Backup file path (empty to skip):")
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		fmt.Fprintln(w.out, "Not running backup.")
		return nil
	}
	return w.exec([]string{"backup", "--file=" + path})
}

func (w *configureWizard) overlay() error {
	w.heading("Overlay")
	fmt.Fprintln(w.out, "This prints the live flannel network. It does not change flannel.")
	if w.state.Check.FlannelMeta == nil {
		fmt.Fprintln(w.out, "flannel metadata is unavailable.")
		return nil
	}
	meta, err := w.state.Check.FlannelMeta()
	if err != nil {
		fmt.Fprintf(w.out, "overlay error=%s\n", err)
		return nil
	}
	cfg, err := flannelConfig(meta)
	if err != nil {
		fmt.Fprintf(w.out, "overlay error=%s\n", err)
		return nil
	}
	addrs := ct.OverlayContainerAddrs(cfg.SubnetLen)
	hosts := flannelHostSubnets(cfg)
	fmt.Fprintf(w.out, "network=%s subnet_len=%d container_addrs=%d host_subnets=%d\n", cfg.Network.String(), cfg.SubnetLen, addrs, hosts)
	fmt.Fprintln(w.out, "Container addresses are 2^(32-subnet length)-3. The host count is flannel's inclusive SubnetMin through SubnetMax. An unset minimum skips the first subnet.")
	return nil
}

func flannelConfig(data []byte) (*subnet.Config, error) {
	var net flanneldisc.Network
	if err := json.Unmarshal(data, &net); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(net.Config)
	if len(raw) == 0 {
		raw = []byte(`{"Network":"` + flannelDefaultNetwork + `"}`)
	}
	var cfg struct {
		Network   string          `json:"Network"`
		SubnetLen uint            `json:"SubnetLen,omitempty"`
		SubnetMin string          `json:"SubnetMin,omitempty"`
		SubnetMax string          `json:"SubnetMax,omitempty"`
		Backend   json.RawMessage `json:"Backend,omitempty"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Network) == "" {
		cfg.Network = flannelDefaultNetwork
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return subnet.ParseConfig(encoded)
}

func flannelHostSubnets(cfg *subnet.Config) int {
	if cfg == nil || cfg.SubnetLen == 0 || cfg.SubnetLen >= 32 {
		return 0
	}
	size := uint32(1) << (32 - cfg.SubnetLen)
	if size == 0 || uint32(cfg.SubnetMax) < uint32(cfg.SubnetMin) {
		return 0
	}
	return int((uint32(cfg.SubnetMax)-uint32(cfg.SubnetMin))/size) + 1
}

func (w *configureWizard) dns() error {
	w.heading("DNS and firewall")
	fmt.Fprintln(w.out, "Text only. Publish low-TTL A records to the hosts.")
	fmt.Fprintln(w.out, "Open ports 80, 443, and 3000-3500 when TCP routes are used.")
	fmt.Fprintln(w.out, "This does not change UFW.")
	return nil
}
