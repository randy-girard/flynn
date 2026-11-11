package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/dbruntime"
)

type modeFileInfo os.FileMode

func (m modeFileInfo) Name() string       { return "" }
func (m modeFileInfo) Size() int64        { return 0 }
func (m modeFileInfo) Mode() os.FileMode  { return os.FileMode(m) }
func (m modeFileInfo) ModTime() time.Time { return time.Time{} }
func (m modeFileInfo) IsDir() bool        { return os.FileMode(m).IsDir() }
func (m modeFileInfo) Sys() any           { return nil }

type stubProductionAPI struct {
	gets          []string
	releases      []string
	profileReads  int
	settingsReads int
	dbReads       int

	tenancy      ct.TenancySettings
	tenancyErr   error
	release      *ct.Release
	releaseErr   error
	profiles     []*ct.RuntimeProfile
	profileErr   error
	resources    []*ct.Resource
	resourcesErr error
	backups      []*ct.TenantBackup
	backupsErr   error
}

func (s *stubProductionAPI) Get(path string, out interface{}) error {
	s.gets = append(s.gets, path)
	switch path {
	case "/tenancy":
		if s.tenancyErr != nil {
			return s.tenancyErr
		}
		dst, ok := out.(*ct.TenancySettings)
		if !ok {
			return fmtUnexpected("tenancy decode")
		}
		*dst = s.tenancy
		return nil
	case "/providers/postgres/resources":
		if s.resourcesErr != nil {
			return s.resourcesErr
		}
		dst, ok := out.(*[]*ct.Resource)
		if !ok {
			return fmtUnexpected("resources decode")
		}
		*dst = s.resources
		return nil
	case "/tenant-backups":
		if s.backupsErr != nil {
			return s.backupsErr
		}
		dst, ok := out.(*[]*ct.TenantBackup)
		if !ok {
			return fmtUnexpected("backups decode")
		}
		*dst = s.backups
		return nil
	default:
		return fmtUnexpected("GET " + path)
	}
}

func (s *stubProductionAPI) GetAppRelease(appID string) (*ct.Release, error) {
	s.releases = append(s.releases, appID)
	if s.releaseErr != nil {
		return nil, s.releaseErr
	}
	return s.release, nil
}

func (s *stubProductionAPI) ListRuntimeProfiles() ([]*ct.RuntimeProfile, error) {
	s.profileReads++
	if s.profileErr != nil {
		return nil, s.profileErr
	}
	return s.profiles, nil
}

func (s *stubProductionAPI) GetRuntimeSettings() (*ct.RuntimeSettings, error) {
	s.settingsReads++
	return &ct.RuntimeSettings{ReserveResources: true}, nil
}

func (s *stubProductionAPI) ListDBRuntimes() (*dbruntime.Catalog, error) {
	s.dbReads++
	return &dbruntime.Catalog{}, nil
}

func fmtUnexpected(msg string) error { return errors.New("unexpected " + msg) }

type productionFixture struct {
	pool         string
	tenancy      ct.TenancySettings
	tenancyErr   error
	env          map[string]string
	releaseErr   error
	profiles     []*ct.RuntimeProfile
	profileErr   error
	hosts        []*cluster.Host
	hostsErr     error
	meta         []byte
	metaErr      error
	zpoolText    string
	zpoolErr     error
	modes        map[string]os.FileMode
	statErr      map[string]error
	color        bool
	resources    []*ct.Resource
	resourcesErr error
	backups      []*ct.TenantBackup
	backupsErr   error
	now          time.Time

	api        *stubProductionAPI
	hostCalls  int
	metaCalls  int
	zpoolCalls int
	zpoolPools []string
	tagBefore  map[string]string
}

func (f *productionFixture) init() {
	if f.pool == "" {
		f.pool = "flynn-default"
	}
	if f.tenancy.Mode == "" && f.tenancyErr == nil {
		f.tenancy.Mode = "hosted"
	}
	if f.env == nil && f.releaseErr == nil {
		// A default name with no parsed type is a Postgres result. This
		// stands in for a hosted cluster already on s3.
		f.env = map[string]string{
			"DEFAULT_BACKEND": "s3main",
			"BACKEND_S3MAIN":  "backend=s3",
		}
	}
	if f.profiles == nil && f.profileErr == nil {
		f.profiles = []*ct.RuntimeProfile{
			{Name: "small", ReserveResources: true},
			{Name: "medium", ReserveResources: true},
			{Name: "large", ReserveResources: true},
		}
	}
	if f.hosts == nil && f.hostsErr == nil {
		f.hosts = []*cluster.Host{hostCap("host-a", "4")}
	}
	if f.meta == nil && f.metaErr == nil {
		f.meta = []byte(`{"config":{"Network":"10.3.0.0/16","SubnetLen":24}}`)
	}
	if f.zpoolText == "" && f.zpoolErr == nil {
		f.zpoolText = zpoolStatusText(
			"NAME STATE READ WRITE CKSUM",
			"flynn-default ONLINE 0 0 0",
			"  /dev/disk/by-id/scsi-disk1 ONLINE 0 0 0",
		)
	}
	if f.modes == nil && f.statErr == nil && f.zpoolErr == nil {
		f.modes = map[string]os.FileMode{"/dev/disk/by-id/scsi-disk1": os.ModeDevice}
	}
}

func (f *productionFixture) run(t *testing.T) (string, error) {
	t.Helper()
	f.init()
	f.api = &stubProductionAPI{
		tenancy:      f.tenancy,
		tenancyErr:   f.tenancyErr,
		release:      &ct.Release{Env: f.env},
		releaseErr:   f.releaseErr,
		profiles:     f.profiles,
		profileErr:   f.profileErr,
		resources:    f.resources,
		resourcesErr: f.resourcesErr,
		backups:      f.backups,
		backupsErr:   f.backupsErr,
	}
	var nowFn func() time.Time
	if !f.now.IsZero() {
		now := f.now
		nowFn = func() time.Time { return now }
	}
	var buf bytes.Buffer
	err := runProductionChecks(&buf, productionCheckConfig{
		Pool: f.pool,
		API:  f.api,
		Now:  nowFn,
		Hosts: func() ([]*cluster.Host, error) {
			f.hostCalls++
			return f.hosts, f.hostsErr
		},
		FlannelMeta: func() ([]byte, error) {
			f.metaCalls++
			return f.meta, f.metaErr
		},
		ZpoolStatus: func(pool string) ([]byte, error) {
			f.zpoolCalls++
			f.zpoolPools = append(f.zpoolPools, pool)
			if f.zpoolErr != nil {
				return []byte(f.zpoolText), f.zpoolErr
			}
			return []byte(f.zpoolText), nil
		},
		Stat: func(name string) (os.FileInfo, error) {
			if err := f.statErr[name]; err != nil {
				return nil, err
			}
			mode, ok := f.modes[name]
			if !ok {
				return nil, os.ErrNotExist
			}
			return modeFileInfo(mode), nil
		},
		Color: f.color,
	})
	return buf.String(), err
}

func hostCap(id, cap string) *cluster.Host {
	tags := map[string]string{}
	if cap != "" {
		tags[ct.FormationMaxProcessesTag] = cap
	}
	return cluster.NewHost(id, "127.0.0.1:1113", nil, tags)
}

func hostNoCap(id string) *cluster.Host {
	return cluster.NewHost(id, "127.0.0.1:1113", nil, map[string]string{"role": "app"})
}

func zpoolStatusText(rows ...string) string {
	var b strings.Builder
	b.WriteString("  pool: flynn-default\n state: ONLINE\nconfig:\n\n")
	for _, row := range rows {
		b.WriteByte('\t')
		b.WriteString(row)
		b.WriteByte('\n')
	}
	b.WriteString("\nerrors: No known data errors\n")
	return b.String()
}

var productionANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func productionRows(t *testing.T, got string) []productionRow {
	t.Helper()
	plain := productionANSI.ReplaceAllString(got, "")
	for _, border := range []string{"┌", "├", "└", "┼", "│"} {
		if !strings.Contains(plain, border) {
			t.Fatalf("missing table border %s\n%s", border, got)
		}
	}
	var rows []productionRow
	sawHeader := false
	for _, line := range strings.Split(strings.TrimSuffix(plain, "\n"), "\n") {
		if !strings.Contains(line, "│") {
			continue
		}
		parts := strings.Split(line, "│")
		if len(parts) != 6 {
			t.Fatalf("row %q has %d cells\n%s", line, len(parts), got)
		}
		cells := []string{
			strings.TrimSpace(parts[1]),
			strings.TrimSpace(parts[2]),
			strings.TrimSpace(parts[3]),
			strings.TrimSpace(parts[4]),
		}
		if !sawHeader {
			if cells[0] != "STATUS" || cells[1] != "CHECK" || cells[2] != "DETAIL" || cells[3] != "WHAT TO DO" {
				t.Fatalf("header %q", line)
			}
			sawHeader = true
			continue
		}
		if cells[0] == "" && cells[1] == "" {
			if len(rows) == 0 {
				t.Fatalf("continuation before a row:\n%s", got)
			}
			rows[len(rows)-1].detail = joinWrapped(rows[len(rows)-1].detail, cells[2])
			rows[len(rows)-1].action = joinWrapped(rows[len(rows)-1].action, cells[3])
			continue
		}
		rows = append(rows, productionRow{
			status: cells[0],
			check:  cells[1],
			detail: cells[2],
			action: cells[3],
		})
	}
	if !sawHeader {
		t.Fatalf("missing table header:\n%s", got)
	}
	return rows
}

func joinWrapped(prev, next string) string {
	next = strings.TrimSpace(next)
	if next == "" {
		return prev
	}
	if prev == "" {
		return next
	}
	return prev + " " + next
}

func requireProductionRows(t *testing.T, got string, want []productionRow) {
	t.Helper()
	if strings.Contains(got, "\033") {
		t.Fatalf("plain run painted color:\n%s", got)
	}
	gotRows := productionRows(t, got)
	if len(gotRows) != len(want) {
		t.Fatalf("row count %d want %d\n%s", len(gotRows), len(want), got)
	}
	for i, w := range want {
		g := gotRows[i]
		if g.status != w.status || g.check != w.check || g.detail != w.detail {
			t.Fatalf("row %d\n got %s %s %s\nwant %s %s %s\n%s", i, g.status, g.check, g.detail, w.status, w.check, w.detail, got)
		}
		if strings.TrimSpace(g.action) == "" {
			t.Fatalf("row %s has no what-to-do\n%s", g.check, got)
		}
		if w.action != "" && !strings.Contains(g.action, w.action) {
			t.Fatalf("row %s action %q missing %q", g.check, g.action, w.action)
		}
	}
}

func mustCheck(t *testing.T, got, status, check, detail string) productionRow {
	t.Helper()
	for _, row := range productionRows(t, got) {
		if row.check != check {
			continue
		}
		if row.status != status || row.detail != detail {
			t.Fatalf("check %s\n got %s %s\nwant %s %s\n%s", check, row.status, row.detail, status, detail, got)
		}
		if strings.TrimSpace(row.action) == "" {
			t.Fatalf("check %s has no what-to-do\n%s", check, got)
		}
		return row
	}
	t.Fatalf("missing check %s\n%s", check, got)
	return productionRow{}
}

func assertFailed(t *testing.T, err error) {
	t.Helper()
	var logged ErrAlreadyLogged
	if !errors.As(err, &logged) {
		t.Fatalf("expected ErrAlreadyLogged, got %v", err)
	}
}

func assertPassed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected exit 0, got %v", err)
	}
}

func blobstoreAction(t *testing.T, got string) string {
	t.Helper()
	for _, row := range productionRows(t, got) {
		if row.check == "blobstore" {
			return row.action
		}
	}
	t.Fatalf("missing blobstore row:\n%s", got)
	return ""
}

// assertBlobstoreMigrateAction fails when the operator action says --migrate
// without --delete. --migrate --delete still contains the substring --migrate,
// so that shorter check is not enough.
func assertBlobstoreMigrateAction(t *testing.T, action string) {
	t.Helper()
	const want = "flynn-host blobstore:set --migrate --delete"
	if !strings.Contains(action, want) {
		t.Fatalf("action %q missing %q", action, want)
	}
	if !strings.Contains(action, "does not migrate") {
		t.Fatalf("action %q does not say the check does not migrate", action)
	}
	if strings.Contains(strings.ReplaceAll(action, "--migrate --delete", ""), "--migrate") {
		t.Fatalf("action has --migrate without --delete: %q", action)
	}
}

func assertProductionWidth(t *testing.T, got string) {
	t.Helper()
	plain := productionANSI.ReplaceAllString(got, "")
	for _, line := range strings.Split(strings.TrimSuffix(plain, "\n"), "\n") {
		// singleton-cap is the widest check name (13). The previous table
		// fit in 96; that name adds four columns.
		if n := len([]rune(line)); n > 100 {
			t.Fatalf("line is %d columns, want at most 100:\n%s", n, line)
		}
	}
}

func TestProductionCheckUsage(t *testing.T) {
	cmd := commands["production:check"]
	if cmd == nil {
		t.Fatal("production:check is not registered")
	}
	line := firstUsageLine(cmd.usage)
	if line != "usage: flynn-host production:check [--zpool-name=<name>]" {
		t.Fatalf("first usage line %q", line)
	}
	for _, want := range []string{
		"--zpool-name=<name>",
		"[default: flynn-default]",
		"RuntimeProfile.ReserveResources",
		"even when that flag is false",
		"does not set flynn-max-processes",
		"parsed type is missing or postgres",
		"never BACKEND_",
		"does not migrate",
		"does not detach",
		"either tenancy mode",
		"Tenancy does not change this line",
	} {
		if !strings.Contains(cmd.usage, want) {
			t.Fatalf("usage missing %q:\n%s", want, cmd.usage)
		}
	}
	if strings.Contains(cmd.usage, "Self-hosted passes a file-backed pool") {
		t.Fatalf("usage still passes a self-hosted file pool:\n%s", cmd.usage)
	}
	parsed := parseHostCLI(t, "production:check", []string{"production:check"})
	if parsed.String["--zpool-name"] != "flynn-default" {
		t.Fatalf("default pool %q", parsed.String["--zpool-name"])
	}
	parsed = parseHostCLI(t, "production:check", []string{"production:check", "--zpool-name=tank"})
	if parsed.String["--zpool-name"] != "tank" {
		t.Fatalf("pool %q", parsed.String["--zpool-name"])
	}
	if got := zpoolStatusArgv("flynn-default"); strings.Join(got, " ") != "zpool status -P flynn-default" {
		t.Fatalf("argv %q", got)
	}
}

func TestProductionCheckSelfHostedPostgresWarnsAndExitsZero(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		env: map[string]string{
			"DEFAULT_BACKEND": "postgres",
			"BACKEND_MINIO":   "backend=minio endpoint=secret.example access_key_id=AKIASECRET bucket=flynnblobstore",
		},
		profiles: []*ct.RuntimeProfile{
			{Name: "small"},
			{Name: "medium"},
			{Name: "large"},
			nil,
			{Name: "  ", ReserveResources: true},
			{Name: "xlarge", ReserveResources: true},
		},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	requireProductionRows(t, got, []productionRow{
		{status: "ok", check: "tenancy", detail: "mode=self_hosted", action: "flynn-host tenancy:mode hosted"},
		{status: "warn", check: "blobstore", detail: "default=postgres", action: "flynn-host blobstore:set --migrate --delete"},
		{status: "ok", check: "zfs", detail: "pool=flynn-default", action: "block device"},
		{status: "ok", check: "reserve", detail: "mode=self_hosted small=false medium=false large=false scheduler=profile_flag", action: "A large cluster runs runtime:reserve"},
		{status: "ok", check: "host-cap", detail: "host-a=4", action: "flynn-max-processes"},
		{status: "warn", check: "singleton-cap", detail: "host-a=missing", action: "flynn-max-persistent-singletons"},
		{status: "warn", check: "backup", detail: "tenant volumes are outside flynn-host backup", action: "Back up tenant volumes separately"},
		{status: "ok", check: "overlay", detail: "network=10.3.0.0/16 subnet_len=24", action: "Informational"},
	})
	if strings.Contains(got, "secret.example") || strings.Contains(got, "AKIASECRET") || strings.Contains(got, "BACKEND_") || strings.Contains(got, "bucket=") {
		t.Fatalf("blobstore line leaked backend env:\n%s", got)
	}
	assertBlobstoreMigrateAction(t, blobstoreAction(t, got))
	if f.api.settingsReads != 0 || f.api.dbReads != 0 {
		t.Fatalf("settings reads %d db reads %d", f.api.settingsReads, f.api.dbReads)
	}
	if f.api.profileReads != 1 || f.hostCalls != 1 || f.metaCalls != 1 || f.zpoolCalls != 1 {
		t.Fatalf("calls profiles=%d hosts=%d meta=%d zpool=%d", f.api.profileReads, f.hostCalls, f.metaCalls, f.zpoolCalls)
	}
	if strings.Join(f.api.gets, ",") != "/tenancy,/providers/postgres/resources" || strings.Join(f.api.releases, ",") != "blobstore" {
		t.Fatalf("gets %v releases %v", f.api.gets, f.api.releases)
	}
	if strings.Join(f.zpoolPools, ",") != "flynn-default" {
		t.Fatalf("zpool pools %v", f.zpoolPools)
	}
	assertProductionWidth(t, got)
}

func TestProductionCheckHostedPostgresFails(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "hosted"},
		env:     map[string]string{},
	}
	got, err := f.run(t)
	assertFailed(t, err)
	requireProductionRows(t, got, []productionRow{
		{status: "ok", check: "tenancy", detail: "mode=hosted", action: "Hosted mode is on"},
		{status: "fail", check: "blobstore", detail: "default=postgres", action: "flynn-host blobstore:set --migrate --delete"},
		{status: "ok", check: "zfs", detail: "pool=flynn-default", action: "block device"},
		{status: "ok", check: "reserve", detail: "mode=hosted small=true medium=true large=true scheduler=hosted_builtin", action: "Hosted placement reserves"},
		{status: "ok", check: "host-cap", detail: "host-a=4", action: "flynn-max-processes"},
		{status: "warn", check: "singleton-cap", detail: "host-a=missing", action: "flynn-max-persistent-singletons"},
		{status: "warn", check: "backup", detail: "tenant volumes are outside flynn-host backup", action: "tenant volumes"},
		{status: "ok", check: "overlay", detail: "network=10.3.0.0/16 subnet_len=24", action: "100.100.0.0/16"},
	})
	assertBlobstoreMigrateAction(t, blobstoreAction(t, got))
}

func TestProductionCheckHostedExternalBackendsPass(t *testing.T) {
	cases := []struct {
		name    string
		detail  string
		env     map[string]string
		secrets []string
	}{
		{
			name:   "gcsmain",
			detail: "default=gcsmain",
			env: map[string]string{
				"DEFAULT_BACKEND":     "gcsmain",
				"BACKEND_GCSMAIN":     "backend=gcs bucket=flynnblobstore",
				"BACKEND_GCSMAIN_KEY": "secret-gcs-json-key",
			},
			secrets: []string{"secret-gcs-json-key", "flynnblobstore"},
		},
		{
			name:   "azuremain",
			detail: "default=azuremain",
			env: map[string]string{
				"DEFAULT_BACKEND":   "azuremain",
				"BACKEND_AZUREMAIN": "backend=azure account_key=secret-azure-key account_name=yyy container=flynnblobstore",
			},
			secrets: []string{"secret-azure-key", "flynnblobstore"},
		},
		{
			name:   "minio",
			detail: "default=minio",
			env: map[string]string{
				"DEFAULT_BACKEND": "minio",
				"BACKEND_MINIO":   "backend=minio endpoint=secret.example access_key_id=AKIASECRET bucket=flynnblobstore",
			},
			secrets: []string{"secret.example", "AKIASECRET", "flynnblobstore"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &productionFixture{tenancy: ct.TenancySettings{Mode: "hosted"}, env: tc.env}
			got, err := f.run(t)
			assertPassed(t, err)
			row := mustCheck(t, got, "ok", "blobstore", tc.detail)
			if !strings.Contains(row.action, "No blobstore change") {
				t.Fatalf("action %q", row.action)
			}
			for _, secret := range append([]string{"BACKEND_", "bucket=", "account_key="}, tc.secrets...) {
				if strings.Contains(got, secret) {
					t.Fatalf("leaked %q:\n%s", secret, got)
				}
			}
			assertProductionWidth(t, got)
		})
	}
}

func TestProductionCheckPostgresTypeFailsOrWarns(t *testing.T) {
	cases := []struct {
		name   string
		detail string
		env    map[string]string
	}{
		{
			name:   "local postgres type",
			detail: "default=local",
			env: map[string]string{
				"DEFAULT_BACKEND": "local",
				"BACKEND_LOCAL":   "backend=postgres endpoint=secret.example access_key_id=AKIASECRET bucket=flynnblobstore",
			},
		},
		{
			name:   "postgres type casing",
			detail: "default=local",
			env: map[string]string{
				"DEFAULT_BACKEND": "local",
				"BACKEND_LOCAL":   "backend=Postgres",
			},
		},
		{
			name:   "missing backend type",
			detail: "default=s3main",
			env: map[string]string{
				"DEFAULT_BACKEND": "s3main",
			},
		},
		{
			name:   "empty backend type",
			detail: "default=local",
			env: map[string]string{
				"DEFAULT_BACKEND": "local",
				"BACKEND_LOCAL":   "bucket=flynnblobstore",
			},
		},
		{
			name:   "postgres name casing",
			detail: "default=postgres",
			env: map[string]string{
				"DEFAULT_BACKEND": "Postgres",
			},
		},
		{
			name:   "empty default",
			detail: "default=postgres",
			env: map[string]string{
				"DEFAULT_BACKEND": "",
			},
		},
	}
	for _, tc := range cases {
		t.Run("hosted/"+tc.name, func(t *testing.T) {
			f := &productionFixture{tenancy: ct.TenancySettings{Mode: "hosted"}, env: tc.env}
			got, err := f.run(t)
			assertFailed(t, err)
			row := mustCheck(t, got, "fail", "blobstore", tc.detail)
			assertBlobstoreMigrateAction(t, row.action)
			if strings.Contains(got, "secret.example") || strings.Contains(got, "AKIASECRET") || strings.Contains(got, "BACKEND_") || strings.Contains(got, "bucket=") {
				t.Fatalf("leaked backend env:\n%s", got)
			}
			assertProductionWidth(t, got)
		})
		t.Run("self_hosted/"+tc.name, func(t *testing.T) {
			f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}, env: tc.env}
			got, err := f.run(t)
			assertPassed(t, err)
			row := mustCheck(t, got, "warn", "blobstore", tc.detail)
			assertBlobstoreMigrateAction(t, row.action)
			if strings.Contains(got, "secret.example") || strings.Contains(got, "AKIASECRET") || strings.Contains(got, "BACKEND_") || strings.Contains(got, "bucket=") {
				t.Fatalf("leaked backend env:\n%s", got)
			}
			assertProductionWidth(t, got)
		})
	}
}

func TestProductionCheckHostedReadyStillWarnsBackup(t *testing.T) {
	f := &productionFixture{
		env: map[string]string{
			"DEFAULT_BACKEND": "s3main",
			"BACKEND_S3MAIN":  "backend=s3 endpoint=secret.example access_key_id=AKIASECRET bucket=flynnblobstore",
		},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	requireProductionRows(t, got, []productionRow{
		{status: "ok", check: "tenancy", detail: "mode=hosted"},
		{status: "ok", check: "blobstore", detail: "default=s3main", action: "No blobstore change"},
		{status: "ok", check: "zfs", detail: "pool=flynn-default"},
		{status: "ok", check: "reserve", detail: "mode=hosted small=true medium=true large=true scheduler=hosted_builtin"},
		{status: "ok", check: "host-cap", detail: "host-a=4"},
		{status: "warn", check: "singleton-cap", detail: "host-a=missing", action: "flynn-max-persistent-singletons"},
		{status: "warn", check: "backup", detail: "tenant volumes are outside flynn-host backup"},
		{status: "ok", check: "overlay", detail: "network=10.3.0.0/16 subnet_len=24"},
	})
	if strings.Contains(got, "secret.example") || strings.Contains(got, "AKIASECRET") || strings.Contains(got, "BACKEND_") {
		t.Fatalf("leaked backend details:\n%s", got)
	}
}

func TestProductionCheckTenancyErrorIsNotSelfHosted(t *testing.T) {
	f := &productionFixture{
		tenancyErr: errors.New("controller down"),
		env:        map[string]string{"DEFAULT_BACKEND": "postgres"},
		profiles: []*ct.RuntimeProfile{
			{Name: "small"},
			{Name: "medium"},
			{Name: "large"},
		},
		zpoolText: zpoolStatusText(
			"NAME STATE READ WRITE CKSUM",
			"flynn-default ONLINE 0 0 0",
			"  /srv/custom.vdev ONLINE 0 0 0",
		),
		modes: map[string]os.FileMode{"/srv/custom.vdev": 0644},
	}
	got, err := f.run(t)
	assertFailed(t, err)
	requireProductionRows(t, got, []productionRow{
		{status: "fail", check: "tenancy", detail: "error=controller down", action: "not self-hosted"},
		{status: "fail", check: "blobstore", detail: "default=postgres", action: "flynn-host blobstore:set --migrate --delete"},
		{status: "fail", check: "zfs", detail: "pool=flynn-default file=/srv/custom.vdev", action: "zpool detach"},
		{status: "fail", check: "reserve", detail: "mode=unknown small=false medium=false large=false scheduler=unknown", action: "not self-hosted"},
		{status: "ok", check: "host-cap", detail: "host-a=4"},
		{status: "warn", check: "singleton-cap", detail: "host-a=missing", action: "flynn-max-persistent-singletons"},
		{status: "warn", check: "backup", detail: "tenant volumes are outside flynn-host backup"},
		{status: "ok", check: "overlay", detail: "network=10.3.0.0/16 subnet_len=24"},
	})
	if f.api.profileReads != 1 || len(f.api.releases) != 1 || f.zpoolCalls != 1 || f.metaCalls != 1 || f.hostCalls != 1 {
		t.Fatalf("short-circuited after tenancy error: profiles=%d releases=%v zpool=%d meta=%d hosts=%d", f.api.profileReads, f.api.releases, f.zpoolCalls, f.metaCalls, f.hostCalls)
	}
	assertBlobstoreMigrateAction(t, blobstoreAction(t, got))
}

func TestProductionCheckHostedReservesWhenFlagsOff(t *testing.T) {
	f := &productionFixture{
		profiles: []*ct.RuntimeProfile{
			{Name: "small"},
			{Name: "medium"},
			{Name: "large"},
		},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	row := mustCheck(t, got, "ok", "reserve", "mode=hosted small=false medium=false large=false scheduler=hosted_builtin")
	if !strings.Contains(row.action, "runtime:reserve") || !strings.Contains(row.action, "stores the flag") {
		t.Fatalf("reserve action %q", row.action)
	}
	assertProductionWidth(t, got)
}

func TestProductionCheckSelfHostedPartialReserve(t *testing.T) {
	f := &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		profiles: []*ct.RuntimeProfile{
			{Name: "small", ReserveResources: true},
			{Name: "medium"},
			{Name: "large"},
			{Name: "xlarge", ReserveResources: true},
		},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	row := mustCheck(t, got, "ok", "reserve", "mode=self_hosted small=true medium=false large=false scheduler=profile_flag")
	if !strings.Contains(row.action, "free reserved CPU and memory") {
		t.Fatalf("reserve action %q", row.action)
	}
}

func TestProductionCheckReservationIgnoresMissingLarge(t *testing.T) {
	f := &productionFixture{
		profiles: []*ct.RuntimeProfile{
			{Name: "Small", ReserveResources: true},
			{Name: "medium", ReserveResources: true},
			{Name: "xlarge", ReserveResources: true},
			{Name: "", ReserveResources: true},
		},
	}
	got, err := f.run(t)
	assertPassed(t, err)
	row := mustCheck(t, got, "ok", "reserve", "mode=hosted small=true medium=true large=false scheduler=hosted_builtin")
	if !strings.Contains(row.action, "runtime:reserve") || !strings.Contains(row.action, "stores the flag") {
		t.Fatalf("reserve action %q", row.action)
	}
}

func TestProductionCheckSingletonCap(t *testing.T) {
	for _, mode := range []string{"hosted", "self_hosted"} {
		f := &productionFixture{tenancy: ct.TenancySettings{Mode: mode}}
		got, err := f.run(t)
		assertPassed(t, err)
		row := mustCheck(t, got, "warn", "singleton-cap", "host-a=missing")
		if !strings.Contains(row.action, "flynn-max-persistent-singletons") {
			t.Fatalf("mode %s action %q", mode, row.action)
		}
	}

	host := hostCap("host-a", "4")
	host.Tags()["flynn-max-persistent-singletons"] = "2"
	before := copyTags(host.Tags())
	f := &productionFixture{hosts: []*cluster.Host{host}}
	got, err := f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "singleton-cap", "host-a=2")
	if !sameTags(host.Tags(), before) {
		t.Fatalf("check changed tags %v -> %v", before, host.Tags())
	}

	unlimited := hostCap("host-b", "4")
	unlimited.Tags()["flynn-max-persistent-singletons"] = "unlimited"
	junk := hostCap("host-a", "4")
	junk.Tags()["flynn-max-persistent-singletons"] = "lots"
	f = &productionFixture{hosts: []*cluster.Host{unlimited, junk}}
	got, err = f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "warn", "singleton-cap", "host-a=lots host-b=unlimited")
}

func TestProductionCheckHostCapSentinels(t *testing.T) {
	cases := []struct {
		cap  string
		want string
	}{
		{"unlimited", "unlimited"},
		{"Unlimited", "unlimited"},
		{"0", "0"},
		{"", "missing"},
		{"off", "off"},
		{"none", "none"},
		{"NONE", "none"},
		{"  ", "empty"},
	}
	for _, tc := range cases {
		var host *cluster.Host
		if tc.cap == "" {
			host = hostNoCap("host-a")
		} else {
			host = hostCap("host-a", tc.cap)
		}
		before := copyTags(host.Tags())
		f := &productionFixture{hosts: []*cluster.Host{host}}
		got, err := f.run(t)
		assertFailed(t, err)
		row := mustCheck(t, got, "fail", "host-cap", "host-a="+tc.want)
		if !strings.Contains(row.action, "flynn-host tags:max-processes") {
			t.Fatalf("cap %q action %q", tc.cap, row.action)
		}
		if !sameTags(host.Tags(), before) {
			t.Fatalf("check changed tags %v -> %v", before, host.Tags())
		}
	}

	f := &productionFixture{hosts: []*cluster.Host{
		hostCap("host-b", "8"),
		hostCap("host-a", "off"),
	}}
	got, err := f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "host-cap", "host-a=off host-b=8")
}

func TestProductionCheckHostAndDiscoverdErrors(t *testing.T) {
	f := &productionFixture{hostsErr: errors.New("object_not_found")}
	got, err := f.run(t)
	assertFailed(t, err)
	row := mustCheck(t, got, "fail", "host-cap", "error=object_not_found")
	if !strings.Contains(row.action, "does not set the tag") {
		t.Fatalf("host error action %q", row.action)
	}
	if f.hostCalls != 1 {
		t.Fatalf("host calls %d", f.hostCalls)
	}

	f = &productionFixture{hosts: []*cluster.Host{}}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "host-cap", "error=no discoverd host registrations")
}

func TestProductionCheckZFS(t *testing.T) {
	mirror := zpoolStatusText(
		"NAME                                                          STATE     READ WRITE CKSUM",
		"flynn-default                                                 ONLINE       0     0     0",
		"  mirror-0                                                    ONLINE       0     0     0",
		"    /srv/custom.vdev                                          ONLINE       0     0     0",
		"    /dev/disk/by-id/scsi-disk1                                ONLINE       0     0     0",
	)
	modes := map[string]os.FileMode{
		"/srv/custom.vdev":           0644,
		"/dev/disk/by-id/scsi-disk1": os.ModeDevice,
	}
	f := &productionFixture{zpoolText: mirror, modes: modes}
	got, err := f.run(t)
	assertFailed(t, err)
	row := mustCheck(t, got, "fail", "zfs", "pool=flynn-default file=/srv/custom.vdev")
	if !strings.Contains(row.action, "does not detach") {
		t.Fatalf("zfs action %q", row.action)
	}
	if strings.Contains(got, "mirror-0") || strings.Contains(got, "flynn-default-zpool.vdev") {
		t.Fatalf("matched the wrong vdev:\n%s", got)
	}

	sparse := zpoolStatusText(
		"NAME STATE READ WRITE CKSUM",
		"flynn-default ONLINE 0 0 0",
		"  /var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev ONLINE 0 0 0",
	)
	f = &productionFixture{
		zpoolText: sparse,
		modes: map[string]os.FileMode{
			"/var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev": 0644,
		},
	}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default file=/var/lib/flynn/volumes/zfs/vdev/flynn-default-zpool.vdev")

	f = &productionFixture{
		tenancy:   ct.TenancySettings{Mode: "self_hosted"},
		zpoolText: mirror,
		modes:     modes,
	}
	got, err = f.run(t)
	assertFailed(t, err)
	row = mustCheck(t, got, "fail", "zfs", "pool=flynn-default file=/srv/custom.vdev")
	if !strings.Contains(row.action, "does not detach") {
		t.Fatalf("self-hosted file action %q", row.action)
	}

	f = &productionFixture{
		zpoolText: mirror,
		modes: map[string]os.FileMode{
			"/srv/custom.vdev":           os.ModeDevice,
			"/dev/disk/by-id/scsi-disk1": os.ModeDevice,
		},
	}
	got, err = f.run(t)
	assertPassed(t, err)
	row = mustCheck(t, got, "ok", "zfs", "pool=flynn-default")
	if !strings.Contains(row.action, "block device") {
		t.Fatalf("block mirror action %q", row.action)
	}

	f = &productionFixture{
		zpoolErr: errors.New("cannot open 'flynn-default': no such pool"),
	}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default error=cannot open 'flynn-default': no such pool")

	f = &productionFixture{
		tenancy:  ct.TenancySettings{Mode: "self_hosted"},
		zpoolErr: errors.New("cannot open 'flynn-default': no such pool"),
	}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default error=cannot open 'flynn-default': no such pool")

	f = &productionFixture{
		zpoolText: zpoolStatusText(
			"NAME STATE READ WRITE CKSUM",
			"flynn-default ONLINE 0 0 0",
			"  /dev/char0 ONLINE 0 0 0",
		),
		modes: map[string]os.FileMode{"/dev/char0": os.ModeDevice | os.ModeCharDevice},
	}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default not-block=/dev/char0")

	f = &productionFixture{
		tenancy: ct.TenancySettings{Mode: "self_hosted"},
		zpoolText: zpoolStatusText(
			"NAME STATE READ WRITE CKSUM",
			"flynn-default ONLINE 0 0 0",
			"  /dev/char0 ONLINE 0 0 0",
		),
		modes: map[string]os.FileMode{"/dev/char0": os.ModeDevice | os.ModeCharDevice},
	}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default not-block=/dev/char0")

	f = &productionFixture{pool: "flynn-default; rm"}
	got, err = f.run(t)
	assertFailed(t, err)
	if f.zpoolCalls != 0 {
		t.Fatalf("zpool invoked for invalid name %d times:\n%s", f.zpoolCalls, got)
	}
	mustCheck(t, got, "fail", "zfs", "pool=flynn-default; rm error=invalid zpool name")
}

func TestProductionCheckOverlay(t *testing.T) {
	f := &productionFixture{meta: []byte(`{"config":{"Network":""}}`)}
	got, err := f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "overlay", "network=100.100.0.0/16 subnet_len=24")
	if strings.Contains(got, "100.64.0.0/16") || strings.Contains(got, "250") {
		t.Fatalf("hardcoded overlay ceiling:\n%s", got)
	}

	f = &productionFixture{meta: []byte(`{"config":{"network":"100.64.0.0/16"}}`)}
	got, err = f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "overlay", "network=100.64.0.0/16 subnet_len=24")

	sub := uint(28)
	raw, err := json.Marshal(map[string]interface{}{
		"config": map[string]interface{}{"Network": "10.3.0.0/16", "SubnetLen": sub},
	})
	if err != nil {
		t.Fatal(err)
	}
	f = &productionFixture{meta: raw}
	got, err = f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "overlay", "network=10.3.0.0/16 subnet_len=28")

	f = &productionFixture{meta: []byte(`{"config":{"Network":"10.9.0.0/24"}}`)}
	got, err = f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "overlay", "network=10.9.0.0/24 subnet_len=25")

	inner := `{"config":{"Network":"10.8.0.0/16","SubnetLen":24},"subnets":{"10.8.1.0-24":{"PublicIP":"192.0.2.10"}}}`
	quoted, err := json.Marshal(inner)
	if err != nil {
		t.Fatal(err)
	}
	f = &productionFixture{meta: quoted}
	got, err = f.run(t)
	assertPassed(t, err)
	mustCheck(t, got, "ok", "overlay", "network=10.8.0.0/16 subnet_len=24")
	if strings.Contains(got, "192.0.2.10") {
		t.Fatalf("double-encoded meta leaked a lease:\n%s", got)
	}

	f = &productionFixture{metaErr: errors.New("flannel meta missing")}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "overlay", "error=flannel meta missing")
	if f.metaCalls != 1 {
		t.Fatalf("meta calls %d", f.metaCalls)
	}
}

func TestProductionCheckBlobstoreAndProfileErrors(t *testing.T) {
	f := &productionFixture{releaseErr: errors.New("no blobstore release")}
	got, err := f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "blobstore", "error=no blobstore release")
	if len(f.api.releases) != 1 || f.api.releases[0] != "blobstore" {
		t.Fatalf("releases %v", f.api.releases)
	}

	f = &productionFixture{env: map[string]string{"BACKEND_X": "not-a-pair", "DEFAULT_BACKEND": "s3main"}}
	got, err = f.run(t)
	assertFailed(t, err)
	var blob productionRow
	for _, candidate := range productionRows(t, got) {
		if candidate.check == "blobstore" {
			blob = candidate
		}
	}
	if blob.status != "fail" || !strings.HasPrefix(blob.detail, "error=") || strings.TrimSpace(blob.action) == "" {
		t.Fatalf("bad backend env:\n%s", got)
	}

	f = &productionFixture{profileErr: errors.New("profile list failed")}
	got, err = f.run(t)
	assertFailed(t, err)
	mustCheck(t, got, "fail", "reserve", "error=profile list failed")
	if f.api.profileReads != 1 {
		t.Fatalf("profile reads %d", f.api.profileReads)
	}
}

func TestProductionCheckColorsStatusWhenRequested(t *testing.T) {
	f := &productionFixture{
		color: true,
		env:   map[string]string{},
	}
	got, err := f.run(t)
	assertFailed(t, err)
	for _, want := range []string{
		"\033[1mSTATUS",
		"\033[1;32mok\033[0m",
		"\033[1;31mfail\033[0m",
		"\033[1;33mwarn\033[0m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\n%s", want, got)
		}
	}
	requireColoredRows(t, got)
}

func requireColoredRows(t *testing.T, got string) {
	t.Helper()
	rows := productionRows(t, got)
	if len(rows) != 8 {
		t.Fatalf("colored table rows %d\n%s", len(rows), got)
	}
	if rows[1].status != "fail" || rows[1].check != "blobstore" || rows[1].detail != "default=postgres" {
		t.Fatalf("colored blobstore row %+v", rows[1])
	}
	assertBlobstoreMigrateAction(t, rows[1].action)
}

func TestProductionCheckBackupLine(t *testing.T) {
	now := time.Date(2026, 11, 11, 16, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour)
	exact := now.Add(-(24*time.Hour + time.Hour))
	stale := exact.Add(-time.Second)
	primary := &ct.Resource{ID: "11111111-1111-1111-1111-111111111111", Env: map[string]string{"FLYNN_POSTGRES_ROLE": "primary"}}
	emptyRole := &ct.Resource{ID: "22222222-2222-2222-2222-222222222222"}
	follower := &ct.Resource{ID: "33333333-3333-3333-3333-333333333333", Env: map[string]string{"FLYNN_POSTGRES_ROLE": "follower"}}
	deposed := &ct.Resource{ID: "44444444-4444-4444-4444-444444444444", Env: map[string]string{"POSTGRES_ROLE": "deposed"}}
	s3 := map[string]string{"DEFAULT_BACKEND": "s3main", "BACKEND_S3MAIN": "backend=s3"}

	t.Run("no primaries stay a warn", func(t *testing.T) {
		f := &productionFixture{tenancy: ct.TenancySettings{Mode: "self_hosted"}, env: s3, resources: []*ct.Resource{follower, deposed}}
		got, err := f.run(t)
		assertPassed(t, err)
		row := mustCheck(t, got, "warn", "backup", "tenant volumes are outside flynn-host backup")
		if !strings.Contains(row.action, "Back up tenant volumes separately") {
			t.Fatal(row.action)
		}
		if strings.Contains(strings.Join(f.api.gets, ","), "/tenant-backups") {
			t.Fatalf("listed dumps with no primary: %v", f.api.gets)
		}
	})

	t.Run("provider not found is no primaries", func(t *testing.T) {
		f := &productionFixture{env: s3, resourcesErr: ct.ErrNotFound}
		got, err := f.run(t)
		assertPassed(t, err)
		mustCheck(t, got, "warn", "backup", "tenant volumes are outside flynn-host backup")
		if strings.Contains(strings.Join(f.api.gets, ","), "/tenant-backups") {
			t.Fatalf("gets %v", f.api.gets)
		}
	})

	for _, mode := range []string{"hosted", "self_hosted"} {
		mode := mode
		t.Run(mode+"/missing dump fails", func(t *testing.T) {
			f := &productionFixture{
				tenancy:   ct.TenancySettings{Mode: mode},
				env:       s3,
				resources: []*ct.Resource{primary, follower},
				now:       now,
			}
			got, err := f.run(t)
			assertFailed(t, err)
			mustCheck(t, got, "fail", "backup", "postgres primaries missing a current dump: 1")
			if strings.Join(f.api.gets, ",") != "/tenancy,/providers/postgres/resources,/tenant-backups" {
				t.Fatalf("gets %v", f.api.gets)
			}
		})
		t.Run(mode+"/fresh dump is ok", func(t *testing.T) {
			f := &productionFixture{
				tenancy:   ct.TenancySettings{Mode: mode},
				env:       s3,
				resources: []*ct.Resource{primary, emptyRole},
				backups: []*ct.TenantBackup{
					{ResourceID: primary.ID, LastSuccessAt: &fresh},
					{ResourceID: emptyRole.ID, LastSuccessAt: &exact},
				},
				now: now,
			}
			got, err := f.run(t)
			assertPassed(t, err)
			mustCheck(t, got, "ok", "backup", "postgres dumps current (2); tenant volumes are outside flynn-host backup")
		})
		t.Run(mode+"/past grace fails", func(t *testing.T) {
			f := &productionFixture{
				tenancy:   ct.TenancySettings{Mode: mode},
				env:       s3,
				resources: []*ct.Resource{primary},
				backups:   []*ct.TenantBackup{{ResourceID: primary.ID, LastSuccessAt: &stale}},
				now:       now,
			}
			got, err := f.run(t)
			assertFailed(t, err)
			mustCheck(t, got, "fail", "backup", "postgres primaries missing a current dump: 1")
		})
	}

	t.Run("status error fails the line", func(t *testing.T) {
		f := &productionFixture{
			env:        s3,
			resources:  []*ct.Resource{primary},
			backupsErr: errors.New("controller down"),
		}
		got, err := f.run(t)
		assertFailed(t, err)
		row := mustCheck(t, got, "fail", "backup", "error=controller down")
		if !strings.Contains(row.action, "does not dump") {
			t.Fatal(row.action)
		}
	})
}

func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sameTags(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
