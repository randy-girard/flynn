package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/blobstore/data"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	discoverd "github.com/randy-girard/flynn/discoverd/client"
	flanneldisc "github.com/randy-girard/flynn/flannel/discoverd"
	"github.com/randy-girard/flynn/flannel/subnet"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/term"
)

// flannelDefaultNetwork is the flannel wrapper's NETWORK when that env var is
// empty (flannel/wrapper/runner.go). 100.64.0.0/16 is the Vagrant install
// value and is reported only when live flannel meta stores it.
const flannelDefaultNetwork = "100.100.0.0/16"

const productionCheckUsage = `
usage: flynn-host production:check [--zpool-name=<name>]

Print a bordered table for tenancy, the blobstore backend, ZFS, runtime
reservation, the per-host process cap, the persistent-singleton cap,
backups, and the overlay. Columns are status, check, detail, and what to
do. Long cells wrap inside the border. Status is green, yellow, or red
on a terminal. Set NO_COLOR to print plain text. The process exits 1
when any status is fail. A warn does not.

Tenancy comes from GET /tenancy. A controller error is a fail. It is not
treated as self_hosted. GetSettings maps only a missing row to self_hosted.

Blobstore reads the blobstore release once and parses DEFAULT_BACKEND
and that name's backend type. Empty DEFAULT_BACKEND is postgres. A name
whose parsed type is missing or postgres, in any case, fails when hosted and warns
when self-hosted. The line prints that default name only, never BACKEND_*
values. The check does not migrate.

ZFS runs zpool status -P for --zpool-name on this host. A regular-file
leaf fails in either tenancy mode, including a mirror that still lists the
file. A block device passes. Tenancy does not change this line. A missing
pool or zpool error fails in either mode. The check does not detach or
destroy vdevs and does not create a pool.

Reservation lists runtime profiles once. Self-hosted placement reads RuntimeProfile.ReserveResources. Hosted mode reserves builtin small, medium, and large even when that flag is false, so this line does not fail a hosted cluster for a false flag. Unknown tenancy fails and is not treated as self-hosted. An empty runtime profile does not reserve. One other profile with the flag does not change the three builtin flags. Database runtimes are not read.

Host cap fails unless every discoverd-registered host has a numeric
flynn-max-processes tag. unlimited, 0, empty, off, and none are no cap.
Hosts() sees only discoverd registrations and can sleep about 45s on
object_not_found. The check does not set flynn-max-processes.

Singleton cap warns in hosted and self-hosted mode unless every
discoverd-registered host has a numeric flynn-max-persistent-singletons
tag. unlimited, 0, empty, off, none, and auto are no cap. A non-integer
is no cap and the detail prints that value. The check does not set the
tag. A host-list error fails this line. Warn does not exit 1.

Postgres, MySQL, and Redis availability each read their own
GET /cluster/<engine>-availability. manual is ok in either tenancy mode.
system with fewer than two registered hosts warns in either tenancy mode
and does not exit 1. system with two or more hosts is ok. These lines do
not query replication lag. A controller error fails that line and is not
treated as manual. Tenancy is not the switch.

Backups warn that tenant volumes are outside flynn-host backup when the
cluster has no Postgres primary. A primary whose last dump is missing, or
strictly older than 24h plus 1h, fails this line in hosted and self-hosted
mode. A dump newer than that is ok, and the detail still says volumes are
outside the tarball. Tenancy is not the switch. The check reads
GET /providers/postgres/resources and, only when a primary exists,
GET /tenant-backups. It does not dump.
Overlay prints the live flannel NETWORK and subnet length. It does not
hardcode a network. This line is informational.

Options:
	--zpool-name=<name>  pool passed to zpool status -P [default: flynn-default]
`

func init() {
	Register("production:check", runProductionCheck, productionCheckUsage)
}

// productionAPI is the controller surface this check reads. It does not
// include runtime settings or database runtimes.
type productionAPI interface {
	Get(path string, out interface{}) error
	GetAppRelease(appID string) (*ct.Release, error)
	ListRuntimeProfiles() ([]*ct.RuntimeProfile, error)
}

type productionAPIError struct {
	err error
}

func (e productionAPIError) Get(string, interface{}) error { return e.err }
func (e productionAPIError) GetAppRelease(string) (*ct.Release, error) {
	return nil, e.err
}
func (e productionAPIError) ListRuntimeProfiles() ([]*ct.RuntimeProfile, error) {
	return nil, e.err
}

// productionCheckConfig is the data the check reads. Tests stub every field.
// A real run uses one tenancy GET, one blobstore release, one profile list,
// one host list, one flannel meta read, and one zpool status.
type productionCheckConfig struct {
	Pool        string
	API         productionAPI
	Hosts       func() ([]*cluster.Host, error)
	FlannelMeta func() ([]byte, error)
	ZpoolStatus func(pool string) ([]byte, error)
	Stat        func(name string) (os.FileInfo, error)
	// Color paints the status column. The CLI sets this from the terminal.
	Color bool
	// Now is the clock for the backup line. Nil uses time.Now.
	Now func() time.Time
}

func runProductionCheck(args *docopt.Args, client *cluster.Client) error {
	pool := strings.TrimSpace(args.String["--zpool-name"])
	var api productionAPI
	if v, err := hostV1(); err != nil {
		api = productionAPIError{err: err}
	} else {
		api = v
	}
	return runProductionChecks(os.Stdout, productionCheckConfig{
		Pool: pool,
		API:  api,
		Hosts: func() ([]*cluster.Host, error) {
			if client == nil {
				return nil, errors.New("no cluster client")
			}
			// Hosts lists discoverd flynn-host registrations only. On
			// object_not_found it retries for about 45s. Tags are read,
			// never written. Q3 writes flynn-max-processes=auto from flynn-host init
			// on a new host.json. The daemon resolves it from host memory and
			// the published runtime sizes. This check still does not set it.
			return client.Hosts()
		},
		FlannelMeta: readFlannelMeta,
		ZpoolStatus: execZpoolStatus,
		Stat:        os.Stat,
		Color:       productionColorEnabled(os.Stdout),
	})
}

func productionColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(f.Fd())
}

func readFlannelMeta() ([]byte, error) {
	meta, err := discoverd.NewService("flannel").GetMeta()
	if err != nil {
		return nil, err
	}
	if meta == nil || len(bytes.TrimSpace(meta.Data)) == 0 {
		return nil, errors.New("flannel meta is empty")
	}
	return append([]byte(nil), meta.Data...), nil
}

func zpoolStatusArgv(pool string) []string {
	return []string{"zpool", "status", "-P", pool}
}

func execZpoolStatus(pool string) ([]byte, error) {
	argv := zpoolStatusArgv(pool)
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return out, errors.New(msg)
	}
	return out, nil
}

var zpoolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func runProductionChecks(w io.Writer, cfg productionCheckConfig) error {
	pool := strings.TrimSpace(cfg.Pool)
	if pool == "" {
		pool = "flynn-default"
	}
	api := cfg.API
	if api == nil {
		api = productionAPIError{err: errors.New("no controller client")}
	}

	var settings ct.TenancySettings
	tenancyErr := api.Get("/tenancy", &settings)

	release, releaseErr := api.GetAppRelease("blobstore")
	profiles, profileErr := api.ListRuntimeProfiles()

	var (
		hosts    []*cluster.Host
		hostsErr error
	)
	if cfg.Hosts == nil {
		hostsErr = errors.New("no host source")
	} else {
		hosts, hostsErr = cfg.Hosts()
	}

	var (
		meta    []byte
		metaErr error
	)
	if cfg.FlannelMeta == nil {
		metaErr = errors.New("no flannel meta source")
	} else {
		meta, metaErr = cfg.FlannelMeta()
	}

	var (
		zpoolOut []byte
		zpoolErr error
		zpoolRun bool
	)
	if zpoolNamePattern.MatchString(pool) {
		zpoolRun = true
		if cfg.ZpoolStatus == nil {
			zpoolErr = errors.New("no zpool status command")
		} else {
			zpoolOut, zpoolErr = cfg.ZpoolStatus(pool)
		}
	}

	mode := "unknown"
	var rows []productionRow
	fail := false
	add := func(row productionRow) {
		if row.status == "fail" {
			fail = true
		}
		rows = append(rows, row)
	}

	if tenancyErr != nil {
		add(productionRow{
			status: "fail",
			check:  "tenancy",
			detail: "error=" + tenancyErr.Error(),
			action: "Fix the controller, then rerun. This is not self-hosted.",
		})
	} else if settings.Mode == tenancy.ModeHosted || settings.Mode == tenancy.ModeSelfHosted {
		mode = settings.Mode
		action := "Hosted mode is on. Quotas apply."
		if mode == tenancy.ModeSelfHosted {
			action = "Quotas are unlimited. Run flynn-host tenancy:mode hosted to switch."
		}
		add(productionRow{status: "ok", check: "tenancy", detail: "mode=" + mode, action: action})
	} else {
		add(productionRow{
			status: "fail",
			check:  "tenancy",
			detail: "error=unknown tenancy mode",
			action: "Set it: flynn-host tenancy:mode self_hosted or hosted.",
		})
	}

	addBlobstoreLine(add, mode, release, releaseErr)
	addZFSLine(add, pool, zpoolRun, zpoolOut, zpoolErr, cfg.Stat)
	addReserveLine(add, mode, profiles, profileErr)
	addHostCapLine(add, hosts, hostsErr)
	addSingletonCapLine(add, hosts, hostsErr)
	addPostgresAvailabilityLine(add, api, hosts, hostsErr)
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "mysql-availability", "/cluster/mysql-availability", "mysql:availability", "The MySQL page shows that.")
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "redis-availability", "/cluster/redis-availability", "redis:availability", "The Redis page shows the offset loss.")
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	addBackupLine(add, api, now)
	addOverlayLine(add, meta, metaErr)

	writeProductionTable(w, rows, cfg.Color)
	if fail {
		return ErrAlreadyLogged{Err: errors.New("production check failed")}
	}
	return nil
}

func addBlobstoreLine(add func(productionRow), mode string, release *ct.Release, err error) {
	if err != nil {
		add(productionRow{
			status: "fail", check: "blobstore", detail: "error=" + err.Error(),
			action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	if release == nil {
		add(productionRow{
			status: "fail", check: "blobstore", detail: "error=blobstore release is empty",
			action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	name, backends, err := data.BackendsFromEnv(data.EnvMapToSlice(release.Env))
	if err != nil {
		add(productionRow{
			status: "fail", check: "blobstore", detail: "error=" + err.Error(),
			action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	if blobstoreDefaultIsPostgres(name, backends) {
		detail := "default=" + name
		if name == "" || strings.EqualFold(name, "postgres") {
			detail = "default=postgres"
		}
		if mode == tenancy.ModeSelfHosted {
			add(productionRow{
				status: "warn", check: "blobstore", detail: detail,
				action: "Postgres is allowed. Hosted needs flynn-host blobstore:set --migrate --delete. This check does not migrate.",
			})
			return
		}
		add(productionRow{
			status: "fail", check: "blobstore", detail: detail,
			action: "Run flynn-host blobstore:set --migrate --delete. This check does not migrate.",
		})
		return
	}
	add(productionRow{
		status: "ok", check: "blobstore", detail: "default=" + name,
		action: "No blobstore change.",
	})
}

const (
	tenantBackupInterval = 24 * time.Hour
	tenantBackupGrace    = time.Hour
)

const backupVolumeDetail = "tenant volumes are outside flynn-host backup"
const backupVolumeAction = "Back up tenant volumes separately."

type backupEngine struct {
	name   string
	path   string
	noun   string
	filter func([]*ct.Resource) []*ct.Resource
}

func addBackupLine(add func(productionRow), api productionAPI, now time.Time) {
	engines := []backupEngine{
		{"postgres", "/providers/postgres/resources", "primaries", postgresBackupResources},
		{"mongodb", "/providers/mongodb/resources", "resources", idBackupResources},
	}
	counted := map[string][]*ct.Resource{}
	total := 0
	for _, eng := range engines {
		var resources []*ct.Resource
		err := api.Get(eng.path, &resources)
		if err != nil {
			if productionNotFound(err) {
				continue
			}
			add(productionRow{
				status: "fail", check: "backup", detail: "error=" + err.Error(),
				action: "Fix the controller, then rerun. This check does not dump.",
			})
			return
		}
		list := eng.filter(resources)
		counted[eng.name] = list
		total += len(list)
	}
	if total == 0 {
		add(productionRow{status: "warn", check: "backup", detail: backupVolumeDetail, action: backupVolumeAction})
		return
	}
	var backups []*ct.TenantBackup
	if err := api.Get("/tenant-backups", &backups); err != nil {
		add(productionRow{
			status: "fail", check: "backup", detail: "error=" + err.Error(),
			action: "Fix the controller, then rerun. This check does not dump.",
		})
		return
	}
	byID := map[string]*ct.TenantBackup{}
	for _, st := range backups {
		if st == nil || st.ResourceID == "" {
			continue
		}
		byID[st.ResourceID] = st
	}
	var misses []string
	var missedNames []string
	var current []string
	otherMiss := false
	for _, eng := range engines {
		list := counted[eng.name]
		if len(list) == 0 {
			continue
		}
		miss := 0
		for _, res := range list {
			var last *time.Time
			if st := byID[res.ID]; st != nil {
				last = st.LastSuccessAt
			}
			if tenantBackupMiss(last, now) {
				miss++
			}
		}
		if miss > 0 {
			misses = append(misses, fmt.Sprintf("%s %s missing a current dump: %d", eng.name, eng.noun, miss))
			missedNames = append(missedNames, eng.name)
			if eng.name != "postgres" {
				otherMiss = true
			}
			continue
		}
		current = append(current, fmt.Sprintf("%s dumps current (%d)", eng.name, len(list)))
	}
	if len(misses) > 0 {
		detail := strings.Join(misses, "; ")
		if otherMiss {
			detail += "; " + backupVolumeDetail
		}
		add(productionRow{
			status: "fail", check: "backup", detail: detail,
			action: backupMissAction(missedNames),
		})
		return
	}
	add(productionRow{
		status: "ok", check: "backup",
		detail: strings.Join(current, "; ") + "; " + backupVolumeDetail,
		action: backupVolumeAction,
	})
}

func backupMissAction(names []string) string {
	var cmds []string
	for _, name := range names {
		switch name {
		case "postgres":
			cmds = append(cmds, "flynn pg dump")
		default:
			cmds = append(cmds, "flynn "+name+" dump")
		}
	}
	verb := "Run " + cmds[0]
	if len(cmds) == 2 {
		verb = "Run " + cmds[0] + " or " + cmds[1]
	} else if len(cmds) > 2 {
		verb = "Run " + strings.Join(cmds[:len(cmds)-1], ", ") + ", or " + cmds[len(cmds)-1]
	}
	return verb + ", or wait for the scheduled dump. This check does not dump. " + backupVolumeAction
}

func postgresBackupResources(resources []*ct.Resource) []*ct.Resource {
	var out []*ct.Resource
	for _, res := range resources {
		if res == nil || strings.TrimSpace(res.ID) == "" || !postgresResourceIsPrimary(res.Env) {
			continue
		}
		out = append(out, res)
	}
	return out
}

func idBackupResources(resources []*ct.Resource) []*ct.Resource {
	var out []*ct.Resource
	for _, res := range resources {
		if res == nil || strings.TrimSpace(res.ID) == "" {
			continue
		}
		out = append(out, res)
	}
	return out
}

func productionNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ct.ErrNotFound) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

func postgresResourceIsPrimary(env map[string]string) bool {
	role := ""
	if env != nil {
		role = strings.TrimSpace(env["FLYNN_POSTGRES_ROLE"])
		if role == "" {
			role = strings.TrimSpace(env["POSTGRES_ROLE"])
		}
	}
	switch strings.ToLower(role) {
	case "follower", "deposed":
		return false
	default:
		return true
	}
}

func tenantBackupMiss(last *time.Time, now time.Time) bool {
	if last == nil || last.IsZero() {
		return true
	}
	return now.UTC().Sub(last.UTC()) > tenantBackupInterval+tenantBackupGrace
}

// blobstoreDefaultIsPostgres is true when the default name is empty or
// postgres, or when that name's parsed backend type is missing or postgres.
// BackendsFromEnv lowercases BACKEND_* names and always inserts a postgres entry.
func blobstoreDefaultIsPostgres(name string, backends map[string]map[string]string) bool {
	if name == "" || strings.EqualFold(name, "postgres") {
		return true
	}
	info, ok := backends[strings.ToLower(name)]
	if !ok || info == nil {
		return true
	}
	kind := info["backend"]
	return kind == "" || strings.EqualFold(kind, "postgres")
}

func addZFSLine(add func(productionRow), pool string, ran bool, out []byte, zerr error, stat func(string) (os.FileInfo, error)) {
	if !ran {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " error=invalid zpool name",
			action: "Use letters, digits, '_', '.', or '-'. zpool was not run.",
		})
		return
	}
	if zerr != nil {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " error=" + zerr.Error(),
			action: "Fix zpool status. This check does not create or destroy the pool.",
		})
		return
	}
	root, leaves, err := parseZpoolDevices(string(out))
	if err != nil {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " error=" + err.Error(),
			action: "Fix zpool status. This check does not create or destroy the pool.",
		})
		return
	}
	if root != pool {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " error=zpool status pool " + root,
			action: "Rerun with --zpool-name set to the printed pool.",
		})
		return
	}
	var files, notBlock []string
	for _, leaf := range leaves {
		if stat == nil {
			add(productionRow{
				status: "fail", check: "zfs", detail: "pool=" + pool + " error=stat " + leaf + ": no stat",
				action: "Stat each leaf on this host. This check does not detach.",
			})
			return
		}
		fi, err := stat(leaf)
		if err != nil {
			add(productionRow{
				status: "fail", check: "zfs", detail: "pool=" + pool + " error=stat " + leaf + ": " + err.Error(),
				action: "Stat each leaf on this host. This check does not detach.",
			})
			return
		}
		kind := vdevKind(fi)
		switch kind {
		case "file":
			files = append(files, leaf)
		case "block":
		default:
			notBlock = append(notBlock, leaf)
		}
	}
	sort.Strings(files)
	sort.Strings(notBlock)
	if len(files) > 0 {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " file=" + strings.Join(files, ","),
			action: "Mirror onto a disk, then zpool detach the file. This check does not detach.",
		})
		return
	}
	if len(notBlock) > 0 {
		add(productionRow{
			status: "fail", check: "zfs", detail: "pool=" + pool + " not-block=" + strings.Join(notBlock, ","),
			action: "Replace that leaf with a block device.",
		})
		return
	}
	add(productionRow{
		status: "ok", check: "zfs", detail: "pool=" + pool,
		action: "Every leaf is a block device.",
	})
}

func vdevKind(fi os.FileInfo) string {
	mode := fi.Mode()
	if mode.IsRegular() {
		return "file"
	}
	if mode&os.ModeDevice != 0 && mode&os.ModeCharDevice == 0 {
		return "block"
	}
	return "other"
}

func parseZpoolDevices(output string) (root string, leaves []string, err error) {
	lines := strings.Split(output, "\n")
	header := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "NAME") && strings.Contains(trim, "STATE") {
			header = i
			break
		}
	}
	if header < 0 {
		return "", nil, errors.New("zpool status has no config")
	}
	type node struct {
		depth int
		name  string
	}
	var nodes []node
	for _, line := range lines[header+1:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			break
		}
		depth, name, ok := vdevRow(line)
		if !ok || name == "NAME" || name == "STATE" {
			continue
		}
		nodes = append(nodes, node{depth: depth, name: name})
	}
	if len(nodes) == 0 {
		return "", nil, errors.New("zpool status has no devices")
	}
	root = nodes[0].name
	rootDepth := nodes[0].depth
	for i, n := range nodes {
		if n.depth == rootDepth {
			continue
		}
		if i+1 < len(nodes) && nodes[i+1].depth > n.depth {
			continue
		}
		leaves = append(leaves, n.name)
	}
	if len(leaves) == 0 {
		return root, nil, errors.New("zpool status has no devices")
	}
	return root, leaves, nil
}

func vdevRow(line string) (depth int, name string, ok bool) {
	rest := line
	if strings.HasPrefix(rest, "\t") {
		rest = rest[1:]
	}
	spaces := 0
	for _, r := range rest {
		if r != ' ' {
			break
		}
		spaces++
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, "", false
	}
	return spaces / 2, fields[0], true
}

func addReserveLine(add func(productionRow), mode string, profiles []*ct.RuntimeProfile, err error) {
	if err != nil {
		add(productionRow{
			status: "fail", check: "reserve", detail: "error=" + err.Error(),
			action: "Fix the runtime profile list, then rerun.",
		})
		return
	}
	// Same name rule as scheduler.syncRuntimeProfiles: lowercased, trimmed.
	// An empty name does not reserve. Hosted placement reserves builtin
	// small, medium, and large even when the stored flag is off.
	flags := map[string]bool{}
	for _, p := range profiles {
		if p == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(p.Name))
		if name == "" {
			continue
		}
		flags[name] = p.ReserveResources
	}
	small := flags["small"]
	medium := flags["medium"]
	large := flags["large"]
	detail := fmt.Sprintf("mode=%s small=%t medium=%t large=%t scheduler=%s", mode, small, medium, large, reserveSchedulerLabel(mode))
	switch mode {
	case tenancy.ModeHosted:
		add(productionRow{
			status: "ok", check: "reserve", detail: detail,
			action: "Hosted placement reserves small, medium, and large. runtime:reserve only stores the flag.",
		})
	case tenancy.ModeSelfHosted:
		action := "Flags may stay off. A large cluster runs runtime:reserve on profiles that must fit."
		if small || medium || large {
			action = "Flagged profiles place only when a host has free reserved CPU and memory."
		}
		add(productionRow{status: "ok", check: "reserve", detail: detail, action: action})
	default:
		add(productionRow{
			status: "fail", check: "reserve", detail: detail,
			action: "Fix the tenancy read, then rerun. Unknown mode is not self-hosted.",
		})
	}
}

func reserveSchedulerLabel(mode string) string {
	switch mode {
	case tenancy.ModeHosted:
		return "hosted_builtin"
	case tenancy.ModeSelfHosted:
		return "profile_flag"
	default:
		return "unknown"
	}
}

func addHostCapLine(add func(productionRow), hosts []*cluster.Host, err error) {
	if err != nil {
		add(productionRow{
			status: "fail", check: "host-cap", detail: "error=" + err.Error(),
			action: "Fix discoverd, then rerun. This check does not set the tag.",
		})
		return
	}
	type capRow struct {
		id    string
		label string
		ok    bool
	}
	caps := make([]capRow, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		label, ok := hostCapLabel(h.Tags())
		caps = append(caps, capRow{id: h.ID(), label: label, ok: ok})
	}
	if len(caps) == 0 {
		add(productionRow{
			status: "fail", check: "host-cap", detail: "error=no discoverd host registrations",
			action: "Register hosts in discoverd, then rerun. This check does not set the tag.",
		})
		return
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].id < caps[j].id })
	parts := make([]string, len(caps))
	allOK := true
	for i, row := range caps {
		if !row.ok {
			allOK = false
		}
		parts[i] = row.id + "=" + row.label
	}
	status := "ok"
	action := "Every registered host has a numeric flynn-max-processes cap."
	if !allOK {
		status = "fail"
		action = "Run flynn-host tags:max-processes <host> <n>, or the same command with --auto to follow runtime sizes. This check does not set the tag."
	}
	add(productionRow{status: status, check: "host-cap", detail: strings.Join(parts, " "), action: action})
}

func addSingletonCapLine(add func(productionRow), hosts []*cluster.Host, err error) {
	if err != nil {
		add(productionRow{
			status: "fail", check: "singleton-cap", detail: "error=" + err.Error(),
			action: "Register hosts in discoverd, then rerun. This check does not set flynn-max-persistent-singletons.",
		})
		return
	}
	type capRow struct {
		id    string
		label string
		ok    bool
	}
	caps := make([]capRow, 0, len(hosts))
	for _, h := range hosts {
		if h == nil {
			continue
		}
		label, ok := singletonCapLabel(h.Tags())
		caps = append(caps, capRow{id: h.ID(), label: label, ok: ok})
	}
	if len(caps) == 0 {
		add(productionRow{
			status: "fail", check: "singleton-cap", detail: "error=no discoverd host registrations",
			action: "Register hosts in discoverd, then rerun. This check does not set flynn-max-persistent-singletons.",
		})
		return
	}
	sort.Slice(caps, func(i, j int) bool { return caps[i].id < caps[j].id })
	parts := make([]string, len(caps))
	allOK := true
	for i, row := range caps {
		if !row.ok {
			allOK = false
		}
		parts[i] = row.id + "=" + row.label
	}
	status := "ok"
	action := "Every registered host has a numeric flynn-max-persistent-singletons cap."
	if !allOK {
		status = "warn"
		action = "Run flynn-host tags:set <host> flynn-max-persistent-singletons=<n> on hosts that should limit new single-node databases. Missing, unlimited, 0, off, none, and auto are no cap. This check does not set the tag. The same warning applies in hosted and self-hosted mode."
	}
	add(productionRow{status: status, check: "singleton-cap", detail: strings.Join(parts, " "), action: action})
}

func singletonCapLabel(tags map[string]string) (string, bool) {
	n, ok := ct.ParseHostPersistentSingletons(tags)
	if ok {
		return strconv.Itoa(n), true
	}
	if tags == nil {
		return "missing", false
	}
	raw, present := tags[ct.FormationMaxPersistentSingletonsTag]
	if !present {
		return "missing", false
	}
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return "empty", false
	case "unlimited", "off", "none", "0", "auto":
		return s, false
	default:
		return s, false
	}
}

func hostCapLabel(tags map[string]string) (string, bool) {
	n, ok := ct.ParseHostMaxProcesses(tags)
	if ok {
		return strconv.Itoa(n), true
	}
	if tags == nil {
		return "missing", false
	}
	raw, present := tags[ct.FormationMaxProcessesTag]
	if !present {
		return "missing", false
	}
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return "empty", false
	case "unlimited", "off", "none", "0":
		return s, false
	default:
		return s, false
	}
}

func addPostgresAvailabilityLine(add func(productionRow), api productionAPI, hosts []*cluster.Host, hostsErr error) {
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "postgres-availability", "/cluster/postgres-availability", "postgres:availability", "The Postgres page shows that.")
}

func addEngineAvailabilityLine(add func(productionRow), api productionAPI, hosts []*cluster.Host, hostsErr error, check, path, command, protected string) {
	var current ct.PostgresAvailability
	if err := api.Get(path, &current); err != nil {
		add(productionRow{
			status: "fail", check: check, detail: "error=" + err.Error(),
			action: "Fix the controller, then rerun. This is not manual.",
		})
		return
	}
	switch current.Mode {
	case "manual":
		add(productionRow{
			status: "ok", check: check, detail: "mode=manual",
			action: "Run flynn-host " + command + " system to create platform replicas. Tenancy does not change this.",
		})
	case "system":
		if hostsErr != nil {
			add(productionRow{
				status: "warn", check: check, detail: "mode=system hosts=unknown",
				action: "Fix the host list, then rerun. A warn does not exit 1.",
			})
			return
		}
		n := len(hosts)
		if n < 2 {
			add(productionRow{
				status: "warn", check: check,
				detail: fmt.Sprintf("mode=system hosts=%d", n),
				action: "Add a second host before a platform replica can leave the primary. This does not fail the cluster.",
			})
			return
		}
		add(productionRow{
			status: "ok", check: check,
			detail: fmt.Sprintf("mode=system hosts=%d", n),
			action: "A primary is protected only while its platform replica is caught up. " + protected,
		})
	default:
		add(productionRow{
			status: "fail", check: check, detail: "error=unknown " + check,
			action: "Set it: flynn-host " + command + " manual or system.",
		})
	}
}

func addOverlayLine(add func(productionRow), meta []byte, err error) {
	if err != nil {
		add(productionRow{
			status: "fail", check: "overlay", detail: "error=" + err.Error(),
			action: "Fix flannel metadata in discoverd, then rerun.",
		})
		return
	}
	network, subnetLen, err := parseFlannelOverlay(meta)
	if err != nil {
		add(productionRow{
			status: "fail", check: "overlay", detail: "error=" + err.Error(),
			action: "Fix flannel metadata in discoverd, then rerun.",
		})
		return
	}
	add(productionRow{
		status: "ok", check: "overlay",
		detail: fmt.Sprintf("network=%s subnet_len=%d", network, subnetLen),
		action: "Informational. Empty NETWORK is 100.100.0.0/16.",
	})
}

type productionRow struct {
	status string
	check  string
	detail string
	action string
}

func writeProductionTable(w io.Writer, rows []productionRow, color bool) {
	headers := []string{"STATUS", "CHECK", "DETAIL", "WHAT TO DO"}
	widths := productionWidths(rows, headers)
	var b strings.Builder
	b.WriteString(boxRule("┌", "┬", "┐", widths))
	b.WriteByte('\n')
	writeBoxLine(&b, headers, widths, func(_ int, cell, padded string) string {
		if color && cell != "" {
			return "\033[1m" + cell + "\033[0m" + padded[len(cell):]
		}
		return padded
	})
	b.WriteByte('\n')
	b.WriteString(boxRule("├", "┼", "┤", widths))
	b.WriteByte('\n')
	for i, row := range rows {
		cols := [][]string{
			wrapCell(row.status, widths[0]),
			wrapCell(row.check, widths[1]),
			wrapCell(row.detail, widths[2]),
			wrapCell(row.action, widths[3]),
		}
		n := 1
		for _, col := range cols {
			if len(col) > n {
				n = len(col)
			}
		}
		for line := 0; line < n; line++ {
			cells := make([]string, len(cols))
			for c, col := range cols {
				if line < len(col) {
					cells[c] = col[line]
				}
			}
			if line > 0 {
				b.WriteByte('\n')
			}
			writeBoxLine(&b, cells, widths, func(col int, cell, padded string) string {
				if color && line == 0 && col == 0 && cell != "" {
					return paintStatus(cell) + padded[len(cell):]
				}
				return padded
			})
		}
		b.WriteByte('\n')
		if i == len(rows)-1 {
			b.WriteString(boxRule("└", "┴", "┘", widths))
		} else {
			b.WriteString(boxRule("├", "┼", "┤", widths))
		}
		b.WriteByte('\n')
	}
	io.WriteString(w, b.String())
}

func productionWidths(rows []productionRow, headers []string) []int {
	widths := []int{6, 9, 32, 36}
	values := [][]string{
		{headers[0]},
		{headers[1]},
		{headers[2]},
		{headers[3]},
	}
	for _, row := range rows {
		values[0] = append(values[0], row.status)
		values[1] = append(values[1], row.check)
		values[2] = append(values[2], row.detail)
		values[3] = append(values[3], row.action)
	}
	for i, col := range values {
		for _, value := range col {
			if n := len(value); i < 2 && n > widths[i] {
				widths[i] = n
			}
			for _, word := range strings.Fields(value) {
				if len(word) > widths[i] {
					widths[i] = len(word)
				}
			}
		}
	}
	return widths
}

func wrapCell(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	cur := ""
	for _, word := range words {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	return append(lines, cur)
}

func writeBoxLine(b *strings.Builder, cells []string, widths []int, paint func(col int, cell, padded string) string) {
	b.WriteString("│")
	for i, cell := range cells {
		padded := padRight(cell, widths[i])
		shown := padded
		if paint != nil {
			shown = paint(i, cell, padded)
		}
		b.WriteByte(' ')
		b.WriteString(shown)
		b.WriteString(" │")
	}
}

func boxRule(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range widths {
		b.WriteString(strings.Repeat("─", w+2))
		if i == len(widths)-1 {
			b.WriteString(right)
		} else {
			b.WriteString(mid)
		}
	}
	return b.String()
}

func paintStatus(status string) string {
	code := "32"
	switch status {
	case "fail":
		code = "31"
	case "warn":
		code = "33"
	}
	return "\033[1;" + code + "m" + status + "\033[0m"
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func parseFlannelOverlay(data []byte) (string, uint, error) {
	var net flanneldisc.Network
	if err := json.Unmarshal(data, &net); err != nil {
		return "", 0, err
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
		return "", 0, err
	}
	if strings.TrimSpace(cfg.Network) == "" {
		cfg.Network = flannelDefaultNetwork
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return "", 0, err
	}
	parsed, err := subnet.ParseConfig(encoded)
	if err != nil {
		return "", 0, err
	}
	return parsed.Network.String(), parsed.SubnetLen, nil
}
