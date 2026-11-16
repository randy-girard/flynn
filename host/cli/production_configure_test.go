package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	router "github.com/randy-girard/flynn/router/types"
)

type cmdLog struct {
	calls [][]string
}

func (c *cmdLog) run(argv []string) error {
	c.calls = append(c.calls, append([]string(nil), argv...))
	return nil
}

func (c *cmdLog) has(name string) bool {
	for _, argv := range c.calls {
		if len(argv) > 0 && argv[0] == name {
			return true
		}
	}
	return false
}

func (c *cmdLog) joined() string {
	var b strings.Builder
	for _, argv := range c.calls {
		b.WriteString(strings.Join(argv, " "))
		b.WriteByte('\n')
	}
	return b.String()
}

func (c *cmdLog) forbid(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if c.has(name) {
			t.Fatalf("called %s:\n%s", name, c.joined())
		}
	}
	if strings.Contains(c.joined(), "zpool") {
		t.Fatalf("invoked zpool:\n%s", c.joined())
	}
}

type scriptAsker struct {
	lines []string
	i     int
}

func (s *scriptAsker) Line(string) (string, error) {
	if s.i >= len(s.lines) {
		return "", nil
	}
	line := s.lines[s.i]
	s.i++
	if line == "CTRL-C" {
		return "", errProductionConfigureStopped
	}
	return line, nil
}

type failAsker struct {
	t *testing.T
}

func (f failAsker) Line(q string) (string, error) {
	f.t.Helper()
	f.t.Fatalf("prompted when a missing TTY must not ask: %s", q)
	return "", nil
}

func configureHeadings(out string) []string {
	var h []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "== ") {
			h = append(h, line)
		}
	}
	return h
}

func wantConfigureHeadings() []string {
	return []string{
		"== Tenancy",
		"== Blobstore",
		"== ZFS",
		"== Host cap",
		"== Runtime reservation",
		"== Log sink",
		"== TLS",
		"== Backup",
		"== Overlay",
		"== DNS and firewall",
		"== production:check",
	}
}

func managedCert(domain string) *string {
	d := domain
	return &d
}

func configureState(f *productionFixture) productionConfigureState {
	if f.hosts == nil && f.hostsErr == nil {
		f.hosts = []*cluster.Host{hostCap("host-a", "4")}
	}
	st := productionConfigureState{Check: f.checkConfig()}
	if st.ACME == nil && f.tenancyErr == nil {
		st.ACME = &ct.ACMEConfig{
			Enabled:              true,
			ContactEmail:         "ops@example.com",
			TermsOfServiceAgreed: true,
			HasAccountKey:        true,
			AccountKey:           "BEGIN PRIVATE KEY leaked-acme",
		}
	}
	cert := "controller.1.localflynn.com"
	st.Domain = "1.localflynn.com"
	st.Apps = []*ct.App{
		{ID: "controller-id", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}},
		{ID: "le-id", Name: "letsencrypt", Meta: map[string]string{"flynn-plugin": "true"}},
	}
	st.Routes = []*router.Route{{
		Type:                     "http",
		ID:                       "r1",
		ParentRef:                ct.RouteParentRefPrefix + "controller-id",
		Domain:                   cert,
		ManagedCertificateDomain: managedCert(cert),
	}}
	return st
}

func runConfigure(t *testing.T, st productionConfigureState, lines []string, tty bool) (string, *cmdLog, error) {
	t.Helper()
	var buf bytes.Buffer
	log := &cmdLog{}
	var ask productionAsker
	if tty {
		ask = &scriptAsker{lines: lines}
	} else {
		ask = failAsker{t: t}
	}
	err := runProductionConfigure(&buf, st, ask, log.run, tty)
	return buf.String(), log, err
}

func TestProductionParentListsCommands(t *testing.T) {
	if commands["production"] == nil || commands["production:configure"] == nil {
		t.Fatal("production and production:configure must be registered")
	}
	help := FormatHelp("production")
	for _, want := range []string{"production:check", "production:configure", "does not start"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	name, args, from := ResolveCommand("production", []string{"configure"})
	if name != "production:configure" || from != "production configure" || len(args) != 0 {
		t.Fatalf("configure alias: name=%q from=%q args=%v", name, from, args)
	}
	name, args, from = ResolveCommand("production", []string{"check", "--zpool-name=tank"})
	if name != "production:check" || from != "production check" || !strings.Contains(strings.Join(args, " "), "--zpool-name=tank") {
		t.Fatalf("check alias: name=%q from=%q args=%v", name, from, args)
	}
	name, _, from = ResolveCommand("production", nil)
	if name != "production" || from != "" {
		t.Fatalf("bare production must not start configure: name=%q from=%q", name, from)
	}
	parsed := parseHostCLI(t, "production:configure", []string{"production:configure"})
	if parsed.String["--zpool-name"] != "flynn-default" {
		t.Fatalf("default pool: %q", parsed.String["--zpool-name"])
	}
	parsed = parseHostCLI(t, "production:configure", []string{"production:configure", "--zpool-name=tank"})
	if parsed.String["--zpool-name"] != "tank" {
		t.Fatalf("pool flag: %q", parsed.String["--zpool-name"])
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := runProduction(nil)
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	r.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(buf.String(), "production:configure") || strings.Contains(buf.String(), "== Tenancy") {
		t.Fatalf("bare production started the wizard or hid configure:\n%s", buf.String())
	}
}

func TestProductionConfigureReadySkipsMutations(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}}
	f.env = map[string]string{
		"DEFAULT_BACKEND": "s3main",
		"BACKEND_S3MAIN":  "backend=s3 bucket=flynnblobstore secret_access_key=leakedsecret",
	}
	st := configureState(f)
	out, log, err := runConfigure(t, st, []string{"no", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "blobstore:set", "blobstore:migrate", "runtime:reserve", "tags:max-processes", "log-sink:add", "letsencrypt:configure", "plugin:install", "tenancy:mode")
	for _, hidden := range []string{"leakedsecret", "BEGIN PRIVATE KEY", "Secret access key", "Blobstore backend", "Process cap for", "Syslog URL", "Let's Encrypt email"} {
		if strings.Contains(out, hidden) {
			t.Fatalf("ready wizard printed %q:\n%s", hidden, out)
		}
	}
	if !strings.Contains(out, "SetBackend") || !strings.Contains(out, "pg_largeobject") {
		t.Fatalf("blobstore note missing:\n%s", out)
	}
	if !strings.Contains(out, "host-a=4") || !strings.Contains(out, "container_addrs=253") || !strings.Contains(out, "host_subnets=255") {
		t.Fatalf("current values missing:\n%s", out)
	}
	if !strings.Contains(out, "80") || !strings.Contains(out, "443") || !strings.Contains(out, "3000") {
		t.Fatalf("dns text missing:\n%s", out)
	}
	if !strings.Contains(out, "follows the stored flag") || !strings.Contains(out, "runtime:reserve only stores the flag") {
		t.Fatalf("reserve explanation missing:\n%s", out)
	}
	if headings := configureHeadings(out); strings.Join(headings, "\n") != strings.Join(wantConfigureHeadings(), "\n") {
		t.Fatalf("headings:\n%v", headings)
	}
}

func TestProductionConfigureSameStepsBothModes(t *testing.T) {
	answers := []string{"no", "skip", ""}
	var outs [2]string
	var errs [2]error
	for i, mode := range []string{"hosted", "self_hosted"} {
		f := &productionFixture{tenancy: ct.TenancySettings{Mode: mode}}
		f.env = map[string]string{"DEFAULT_BACKEND": "postgres"}
		st := configureState(f)
		outs[i], _, errs[i] = runConfigure(t, st, answers, true)
	}
	if strings.Join(configureHeadings(outs[0]), "\n") != strings.Join(configureHeadings(outs[1]), "\n") {
		t.Fatalf("steps differ\nhosted:\n%s\nself:\n%s", strings.Join(configureHeadings(outs[0]), "\n"), strings.Join(configureHeadings(outs[1]), "\n"))
	}
	if !strings.Contains(outs[0], "fails Postgres") || strings.Contains(outs[0], "warns on Postgres") {
		t.Fatalf("hosted sentence:\n%s", outs[0])
	}
	if !strings.Contains(outs[1], "warns on Postgres") || strings.Contains(outs[1], "fails Postgres") {
		t.Fatalf("self-hosted sentence:\n%s", outs[1])
	}
	if errs[0] == nil || errs[1] != nil {
		t.Fatalf("hosted err=%v self err=%v", errs[0], errs[1])
	}
}

func TestProductionConfigurePostgresWaitsForConfirm(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}}
	f.env = map[string]string{"DEFAULT_BACKEND": "postgres"}
	st := configureState(f)
	out, log, err := runConfigure(t, st, []string{"no", "s3", "b", "us-east-1", "AKI", "supersecret", "", "no", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "blobstore:set", "blobstore:migrate")
	if strings.Contains(out, "supersecret") {
		t.Fatalf("printed secret:\n%s", out)
	}

	out, log, err = runConfigure(t, st, []string{"no", "s3", "b", "us-east-1", "AKI", "supersecret", "", "yes", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !log.has("blobstore:set") {
		t.Fatalf("confirm did not set:\n%s", log.joined())
	}
	got := log.joined()
	for _, want := range []string{"--backend=s3", "--bucket=b", "--region=us-east-1", "--access-key-id=AKI", "--secret-access-key=supersecret", "--migrate", "--delete"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s:\n%s", want, got)
		}
	}
	if log.has("region") || log.has("region:link") || strings.Contains(out, "supersecret") {
		t.Fatalf("region command or printed secret:\n%s\n%s", got, out)
	}
	if !strings.Contains(out, "cluster_settings.region") {
		t.Fatalf("region note missing:\n%s", out)
	}
}

func TestProductionConfigureGCSMigrateAfterConfirm(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "hosted"}}
	f.env = map[string]string{}
	st := configureState(f)
	_, log, _ := runConfigure(t, st, []string{"no", "gcs", "no", ""}, true)
	log.forbid(t, "blobstore:set", "blobstore:migrate")

	out, log, _ := runConfigure(t, st, []string{"no", "gcs", "yes", ""}, true)
	if log.has("blobstore:set") || !log.has("blobstore:migrate") || !strings.Contains(log.joined(), "--delete") {
		t.Fatalf("gcs migrate: %s", log.joined())
	}
	if !strings.Contains(out, "BACKEND_GCSMAIN") || !strings.Contains(out, "env:set") {
		t.Fatalf("gcs path missing:\n%s", out)
	}
}

func TestProductionConfigureFilePoolDoesNotRunZpool(t *testing.T) {
	file := "/var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev"
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "hosted"},
		zpoolText: zpoolStatusText(
			"NAME STATE READ WRITE CKSUM",
			"flynn-default ONLINE 0 0 0",
			"  "+file+" ONLINE 0 0 0",
		),
		modes: map[string]os.FileMode{file: 0o644},
	}
	st := configureState(f)
	out, log, err := runConfigure(t, st, []string{"no", ""}, true)
	log.forbid(t, "blobstore:set")
	if !strings.Contains(out, "zpool attach") || !strings.Contains(out, file) || !strings.Contains(out, "each host") {
		t.Fatalf("procedure missing:\n%s", out)
	}
	if err == nil {
		t.Fatal("file-backed pool must still fail production:check")
	}
}

func TestProductionConfigureMissingPoolContinues(t *testing.T) {
	f := &productionFixture{
		tenancy:  ct.TenancySettings{Mode: "self_hosted"},
		zpoolErr: errors.New("cannot open 'flynn-default': no such pool"),
	}
	st := configureState(f)
	out, log, err := runConfigure(t, st, []string{"no", ""}, true)
	log.forbid(t)
	if !strings.Contains(out, "no such pool") || !strings.Contains(out, "== Backup") {
		t.Fatalf("missing pool did not continue:\n%s", out)
	}
	if err == nil {
		t.Fatal("missing pool fails the check")
	}
}

func TestProductionConfigureHostCapAsksForNumber(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		hosts: []*cluster.Host{
			hostCap("host-a", "4"),
			hostCap("host-b", "unlimited"),
			hostCap("host-c", "auto"),
		},
	}
	st := configureState(f)
	_, log, _ := runConfigure(t, st, []string{"no", "0", "9", ""}, true)
	if strings.Contains(log.joined(), "host-a") || strings.Contains(log.joined(), "host-b") {
		t.Fatalf("capped or rejected host was set:\n%s", log.joined())
	}
	if !strings.Contains(log.joined(), "tags:max-processes host-c 9") {
		t.Fatalf("auto host: %s", log.joined())
	}
}

func TestProductionConfigureReserveBothModes(t *testing.T) {
	for _, mode := range []string{"hosted", "self_hosted"} {
		f := &productionFixture{
			tenancy: ct.TenancySettings{Mode: mode},
			profiles: []*ct.RuntimeProfile{
				{Name: "small", ReserveResources: true},
				{Name: "Medium", ReserveResources: false},
				{Name: "large", ReserveResources: false},
			},
		}
		st := configureState(f)
		out, log, err := runConfigure(t, st, []string{"no", "yes", ""}, true)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if strings.Contains(log.joined(), "runtime:reserve small") || !strings.Contains(log.joined(), "runtime:reserve medium") || !strings.Contains(log.joined(), "runtime:reserve large") {
			t.Fatalf("%s reserve: %s", mode, log.joined())
		}
		if !strings.Contains(out, "even when reserve_resources is false") || !strings.Contains(out, "follows the stored flag") {
			t.Fatalf("%s explanation:\n%s", mode, out)
		}
	}
}

func TestProductionConfigureLogSink(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		sinks: []*ct.Sink{syslogSink("app", "app-1", ct.SyslogSinkConfig{
			URL: "syslog://logs.example:514/", Format: ct.SyslogFormatRFC6587,
		})},
	}
	st := configureState(f)
	_, log, err := runConfigure(t, st, []string{"no", "http://logs.example/v1", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "log-sink:add")

	_, log, err = runConfigure(t, st, []string{"no", "syslog://logs.example:514/", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.joined(), "log-sink:add syslog syslog://logs.example:514/") {
		t.Fatalf("add: %s", log.joined())
	}

	f.sinks = []*ct.Sink{syslogSink("cluster", "", ct.SyslogSinkConfig{
		URL: "syslog://logs.example:514/", Format: ct.SyslogFormatRFC6587, Scope: ct.SinkScopeAll,
	})}
	st = configureState(f)
	out, log, err := runConfigure(t, st, []string{"no", "syslog://logs.example:514/", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "log-sink:add")
	if !strings.Contains(out, "warn") {
		t.Fatalf("self-hosted syslog:// can finish the step and still warn:\n%s", out)
	}
}

func TestProductionConfigureTLS(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}}
	st := configureState(f)
	st.ACME = &ct.ACMEConfig{}
	st.Apps = []*ct.App{{ID: "controller-id", Name: "controller", Meta: map[string]string{"flynn-system-app": "true"}}}
	st.Routes = []*router.Route{{
		Type: "http", ID: "r1", ParentRef: ct.RouteParentRefPrefix + "controller-id", Domain: "controller.1.localflynn.com",
	}}
	_, log, err := runConfigure(t, st, []string{"no", "no", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "plugin:install", "letsencrypt:configure", "letsencrypt:enable-system-routes")

	out, log, err := runConfigure(t, st, []string{"no", "yes", "ops@example.com", "no", "yes", "yes", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	got := log.joined()
	if !strings.Contains(got, "plugin:install letsencrypt") || !strings.Contains(got, "letsencrypt:configure --email=ops@example.com --agree-tos") || strings.Contains(got, "--staging") {
		t.Fatalf("tls commands: %s", got)
	}
	if !strings.Contains(got, "letsencrypt:enable-system-routes") {
		t.Fatalf("system routes: %s", got)
	}
	if strings.Contains(out, "BEGIN PRIVATE") {
		t.Fatalf("printed account key:\n%s", out)
	}

	_, log, _ = runConfigure(t, st, []string{"no", "yes", "ops@example.com", "yes", "yes", "no", ""}, true)
	if !strings.Contains(log.joined(), "--staging") || strings.Contains(log.joined(), "letsencrypt:enable-system-routes") {
		t.Fatalf("staging/routes: %s", log.joined())
	}
}

func TestProductionConfigureBackupPath(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}}
	st := configureState(f)
	_, log, err := runConfigure(t, st, []string{"no", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "backup")
	out, log, err := runConfigure(t, st, []string{"no", "/tmp/p4.tar"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.joined(), "backup") || !strings.Contains(log.joined(), "/tmp/p4.tar") {
		t.Fatalf("backup: %s", log.joined())
	}
	if !strings.Contains(out, "tenant volumes") {
		t.Fatalf("backup meaning:\n%s", out)
	}
	_, log, err = runConfigure(t, st, []string{"no", "/tmp/p4-later.tar"}, true)
	if err != nil || !strings.Contains(log.joined(), "/tmp/p4-later.tar") {
		t.Fatalf("later backup: %v %s", err, log.joined())
	}
}

func TestProductionConfigureNoTTYAppliesNothing(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		env:     map[string]string{"DEFAULT_BACKEND": "postgres"},
		profiles: []*ct.RuntimeProfile{
			{Name: "small", ReserveResources: false},
			{Name: "medium", ReserveResources: false},
			{Name: "large", ReserveResources: false},
		},
		hosts: []*cluster.Host{hostCap("host-a", "unlimited")},
		sinks: []*ct.Sink{},
	}
	st := configureState(f)
	st.ACME = nil
	st.Apps = nil
	st.Routes = nil
	out, log, _ := runConfigure(t, st, []string{"yes", "s3", "b", "us-east-1", "AKI", "supersecret", "", "yes", "40", "yes", "syslog://logs.example:514/", "yes", "ops@example.com", "yes", "yes", "yes", "/tmp/p4.tar"}, false)
	log.forbid(t, "blobstore:set", "blobstore:migrate", "runtime:reserve", "tags:max-processes", "log-sink:add", "letsencrypt:configure", "plugin:install", "backup", "tenancy:mode")
	if !strings.Contains(out, "no TTY") {
		t.Fatalf("missing TTY note:\n%s", out)
	}
}

func TestProductionConfigureStopLeavesEarlierSteps(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		env:     map[string]string{"DEFAULT_BACKEND": "postgres"},
	}
	st := configureState(f)
	out, log, err := runConfigure(t, st, []string{"yes", "hosted", "CTRL-C"}, true)
	if !errors.Is(err, errProductionConfigureStopped) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(log.joined(), "tenancy:mode hosted") || log.has("blobstore:set") {
		t.Fatalf("stop: %s", log.joined())
	}
	if strings.Contains(out, "== production:check") {
		t.Fatalf("check ran after stop:\n%s", out)
	}
}

func TestProductionConfigureTenancyMatchDoesNotCall(t *testing.T) {
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "hosted"}}
	st := configureState(f)
	_, log, err := runConfigure(t, st, []string{"yes", "hosted", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	log.forbid(t, "tenancy:mode")
}

func TestProductionConfigureOverlayCeilings(t *testing.T) {
	raw := []byte(`{"config":{"Network":"10.3.0.0/16","SubnetLen":28}}`)
	f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}, meta: raw}
	st := configureState(f)
	out, _, err := runConfigure(t, st, []string{"no", ""}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "network=10.3.0.0/16") || !strings.Contains(out, "subnet_len=28") || !strings.Contains(out, "container_addrs=13") || !strings.Contains(out, "host_subnets=4095") {
		t.Fatalf("ceilings:\n%s", out)
	}
}

func TestProductionConfigureSkippedStepIsAskedAgain(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		env:     map[string]string{"DEFAULT_BACKEND": "postgres"},
	}
	st := configureState(f)
	for i := 0; i < 2; i++ {
		out, log, err := runConfigure(t, st, []string{"no", "skip", ""}, true)
		if err != nil {
			t.Fatal(err)
		}
		log.forbid(t, "blobstore:set")
		if !strings.Contains(out, "Blobstore backend") {
			t.Fatalf("run %d did not ask:\n%s", i, out)
		}
	}
}

func TestProductionConfigureDocs(t *testing.T) {
	for _, path := range []string{"../../docs/content/production.html.md", "../../docs/content/cli.md"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.Contains(text, "production:configure") || !strings.Contains(text, "safe to re-run") {
			t.Fatalf("%s does not describe production:configure as safe to re-run", path)
		}
	}
}
