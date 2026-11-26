package productioncheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/randy-girard/flynn/blobstore/backendenv"
	"github.com/randy-girard/flynn/controller/authorizer"
	"github.com/randy-girard/flynn/controller/placement"
	"github.com/randy-girard/flynn/controller/servicetoken"
	"github.com/randy-girard/flynn/controller/tenancy"
	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/cluster"
	"github.com/randy-girard/flynn/pkg/updaterdeploy"
)

// API is the controller surface this check reads. It does not
// include runtime settings or database runtimes.
type API interface {
	Get(path string, out interface{}) error
	GetAppRelease(appID string) (*ct.Release, error)
	ListRuntimeProfiles() ([]*ct.RuntimeProfile, error)
}

// addSireniaFormationLine checks that every sirenia app's engines run from
// its current release (EDGE-73). A current release with no engines while
// another release runs them fails: every deploy copies the current formation,
// so before EDGE-73 a deploy left the engines where they were. Another
// release's formation that still asks for engines while the primary is on
// the current release warns (EDGE-76: a rollback kept its peers). Engines on
// another release while the current release also runs some warns (it is
// also the shape during a sirenia deploy).
func addSireniaFormationLine(add func(Row), states []updaterdeploy.SireniaFormationState, err error) {
	if err != nil {
		add(Row{
			Status: "fail", Check: "sirenia-formation", Detail: "error=" + err.Error(),
			Action: "Fix the controller or discoverd, then rerun.",
		})
		return
	}
	short := func(id string) string {
		if len(id) > 8 {
			return id[:8]
		}
		return id
	}
	also := func(s updaterdeploy.SireniaFormationState) string {
		ids := make([]string, 0, len(s.Others))
		for id := range s.Others {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := ""
		for _, id := range ids {
			out += fmt.Sprintf(" also=%s %s=%d", short(id), s.ProcessType, s.Others[id])
		}
		return out
	}
	bad := false
	for _, s := range states {
		if !s.Mismatch() {
			if len(s.Others) == 0 {
				continue
			}
			// EDGE-76: a rollback kept a failed release's engines.
			bad = true
			add(Row{
				Status: "warn", Check: "sirenia-formation",
				Detail: fmt.Sprintf("%s current=%s %s=%d%s", s.App, short(s.CurrentRelease), s.ProcessType, s.CurrentCount, also(s)),
				Action: "Engines run from more than one release. Unless a deploy of the app is running, run flynn-host update (or deploy the app); its orphan repair scales the other releases down.",
			})
			continue
		}
		bad = true
		detail := fmt.Sprintf("%s current=%s %s=%d engines=%s %s=%d", s.App, short(s.CurrentRelease), s.ProcessType, s.CurrentCount, short(s.PrimaryRelease), s.ProcessType, s.PrimaryCount) + also(s)
		if s.Stuck() {
			add(Row{
				Status: "fail", Check: "sirenia-formation", Detail: detail,
				Action: "The current release runs no engines. Unless a deploy of the app is running, run flynn-host update (or deploy the app) to move them to a new release. Do not scale the current release by hand.",
			})
			continue
		}
		add(Row{
			Status: "warn", Check: "sirenia-formation", Detail: detail,
			Action: "The primary is not on the current release. Unless a deploy of the app is running, run flynn-host update (or deploy the app) to move the engines to one release.",
		})
	}
	if !bad {
		add(Row{
			Status: "ok", Check: "sirenia-formation", Detail: fmt.Sprintf("apps=%d", len(states)),
			Action: "Every sirenia app's primary and engines run from its current release.",
		})
	}
}

func addBlobstoreLine(add func(Row), mode string, release *ct.Release, err error) {
	if err != nil {
		add(Row{
			Status: "fail", Check: "blobstore", Detail: "error=" + err.Error(),
			Action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	if release == nil {
		add(Row{
			Status: "fail", Check: "blobstore", Detail: "error=blobstore release is empty",
			Action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	name, backends, err := backendenv.BackendsFromEnv(backendenv.EnvMapToSlice(release.Env))
	if err != nil {
		add(Row{
			Status: "fail", Check: "blobstore", Detail: "error=" + err.Error(),
			Action: "Fix the blobstore release, then rerun. This check does not migrate.",
		})
		return
	}
	if BlobstoreDefaultIsPostgres(name, backends) {
		detail := "default=" + name
		if name == "" || strings.EqualFold(name, "postgres") {
			detail = "default=postgres"
		}
		if mode == tenancy.ModeSelfHosted {
			add(Row{
				Status: "warn", Check: "blobstore", Detail: detail,
				Action: "Postgres is allowed. Hosted needs flynn-host blobstore:set --migrate --delete. This check does not migrate.",
			})
			return
		}
		add(Row{
			Status: "fail", Check: "blobstore", Detail: detail,
			Action: "Run flynn-host blobstore:set --migrate --delete. This check does not migrate.",
		})
		return
	}
	add(Row{
		Status: "ok", Check: "blobstore", Detail: "default=" + name,
		Action: "No blobstore change.",
	})
}

// blobstoreAllowUnauthenticatedEnv is the blobstore's explicit opt-in to
// serve objects with no credentials (blobstore/auth.go).
const blobstoreAllowUnauthenticatedEnv = "BLOBSTORE_ALLOW_UNAUTHENTICATED"

// addBlobstoreAuthLine reports whether the blobstore release can authenticate
// callers. Without AUTH_KEY or CONTROLLER_KEY and without ACCESS_TOKEN_KEY
// the blobstore refuses every object request (EDGE-12); with the
// BLOBSTORE_ALLOW_UNAUTHENTICATED override it serves every object to anyone.
// Both fail in either tenancy mode. Only key names are printed, never values.
// A release read error is already a blobstore fail and adds no row here.
func addBlobstoreAuthLine(add func(Row), release *ct.Release, err error) {
	if err != nil || release == nil {
		return
	}
	env := release.Env
	clusterKey := "missing"
	for _, name := range []string{"AUTH_KEY", "CONTROLLER_KEY"} {
		for _, k := range strings.Split(env[name], ",") {
			if strings.TrimSpace(k) != "" {
				clusterKey = "set"
			}
		}
	}
	tokenKey := "missing"
	if raw := strings.TrimSpace(env["ACCESS_TOKEN_KEY"]); raw != "" {
		tokenKey = "set"
		if k, err := authorizer.ParseTokenKey(raw); err != nil || k == nil {
			tokenKey = "invalid"
		}
	}
	override := false
	switch strings.ToLower(strings.TrimSpace(env[blobstoreAllowUnauthenticatedEnv])) {
	case "1", "true":
		override = true
	}
	detail := "cluster_key=" + clusterKey + " token_key=" + tokenKey
	if override {
		detail += " unauthenticated=allowed"
	}
	row := Row{Check: "blobstore-auth", Detail: detail}
	switch {
	case clusterKey == "missing" && tokenKey != "set" && override:
		row.Status = "fail"
		row.Action = "Every object is open to any caller. Set AUTH_KEY and ACCESS_TOKEN_KEY on the blobstore release and unset " + blobstoreAllowUnauthenticatedEnv + "."
	case clusterKey == "missing" && tokenKey != "set":
		row.Status = "fail"
		row.Action = "The blobstore refuses every object request, so builds and deploys fail. Set AUTH_KEY (the controller key) and ACCESS_TOKEN_KEY on the blobstore release."
	case clusterKey == "missing":
		row.Status = "fail"
		row.Action = "Cluster services authenticate with the controller key and are refused. Set AUTH_KEY on the blobstore release."
	case override:
		row.Status = "warn"
		row.Action = "Unset " + blobstoreAllowUnauthenticatedEnv + ". The keys are enforced now, but it opens every object if they are removed."
	case tokenKey != "set":
		row.Status = "warn"
		row.Action = "scoped build tokens are refused, so app builds fail. Set a valid ACCESS_TOKEN_KEY on the blobstore release."
	default:
		row.Status = "ok"
		row.Action = "No blobstore auth change."
	}
	add(row)
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

func addBackupLine(add func(Row), api API, now time.Time) {
	engines := []backupEngine{
		{"postgres", "/providers/postgres/resources", "primaries", postgresBackupResources},
		{"mongodb", "/providers/mongodb/resources", "resources", idBackupResources},
		{"kafka", "/providers/kafka/resources", "resources", idBackupResources},
		{"clickhouse", "/providers/clickhouse/resources", "resources", idBackupResources},
	}
	counted := map[string][]*ct.Resource{}
	total := 0
	for _, eng := range engines {
		var resources []*ct.Resource
		err := api.Get(eng.path, &resources)
		if err != nil {
			if NotFound(err) {
				continue
			}
			add(Row{
				Status: "fail", Check: "backup", Detail: "error=" + err.Error(),
				Action: "Fix the controller, then rerun. This check does not dump.",
			})
			return
		}
		list := eng.filter(resources)
		counted[eng.name] = list
		total += len(list)
	}
	if total == 0 {
		add(Row{Status: "warn", Check: "backup", Detail: backupVolumeDetail, Action: backupVolumeAction})
		return
	}
	var backups []*ct.TenantBackup
	if err := api.Get("/tenant-backups", &backups); err != nil {
		add(Row{
			Status: "fail", Check: "backup", Detail: "error=" + err.Error(),
			Action: "Fix the controller, then rerun. This check does not dump.",
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
		add(Row{
			Status: "fail", Check: "backup", Detail: detail,
			Action: backupMissAction(missedNames),
		})
		return
	}
	add(Row{
		Status: "ok", Check: "backup",
		Detail: strings.Join(current, "; ") + "; " + backupVolumeDetail,
		Action: backupVolumeAction,
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

func NotFound(err error) bool {
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

// BlobstoreDefaultIsPostgres is true when the default name is empty or
// postgres, or when that name's parsed backend type is missing or postgres.
// BackendsFromEnv lowercases BACKEND_* names and always inserts a postgres entry.
func BlobstoreDefaultIsPostgres(name string, backends map[string]map[string]string) bool {
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

func addReserveLine(add func(Row), mode string, profiles []*ct.RuntimeProfile, err error) {
	if err != nil {
		add(Row{
			Status: "fail", Check: "reserve", Detail: "error=" + err.Error(),
			Action: "Fix the runtime profile list, then rerun.",
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
		add(Row{
			Status: "ok", Check: "reserve", Detail: detail,
			Action: "Hosted placement reserves small, medium, and large. runtime:reserve only stores the flag.",
		})
	case tenancy.ModeSelfHosted:
		action := "Flags may stay off. A large cluster runs runtime:reserve on profiles that must fit."
		if small || medium || large {
			action = "Flagged profiles place only when a host has free reserved CPU and memory."
		}
		add(Row{Status: "ok", Check: "reserve", Detail: detail, Action: action})
	default:
		add(Row{
			Status: "fail", Check: "reserve", Detail: detail,
			Action: "Fix the tenancy read, then rerun. Unknown mode is not self-hosted.",
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

func addHostCapLine(add func(Row), hosts []*cluster.Host, err error) {
	if err != nil {
		add(Row{
			Status: "fail", Check: "host-cap", Detail: "error=" + err.Error(),
			Action: "Fix discoverd, then rerun. This check does not set the tag.",
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
		label, ok := HostCapLabel(h.Tags())
		caps = append(caps, capRow{id: h.ID(), label: label, ok: ok})
	}
	if len(caps) == 0 {
		add(Row{
			Status: "fail", Check: "host-cap", Detail: "error=no discoverd host registrations",
			Action: "Register hosts in discoverd, then rerun. This check does not set the tag.",
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
	add(Row{Status: status, Check: "host-cap", Detail: strings.Join(parts, " "), Action: action})
}

func addSingletonCapLine(add func(Row), hosts []*cluster.Host, err error) {
	if err != nil {
		add(Row{
			Status: "fail", Check: "singleton-cap", Detail: "error=" + err.Error(),
			Action: "Register hosts in discoverd, then rerun. This check does not set flynn-max-persistent-singletons.",
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
		add(Row{
			Status: "fail", Check: "singleton-cap", Detail: "error=no discoverd host registrations",
			Action: "Register hosts in discoverd, then rerun. This check does not set flynn-max-persistent-singletons.",
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
	add(Row{Status: status, Check: "singleton-cap", Detail: strings.Join(parts, " "), Action: action})
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

func HostCapLabel(tags map[string]string) (string, bool) {
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

func addPostgresAvailabilityLine(add func(Row), api API, hosts []*cluster.Host, hostsErr error) {
	addEngineAvailabilityLine(add, api, hosts, hostsErr, "postgres-availability", "/cluster/postgres-availability", "postgres:availability", 2, "The Postgres page shows that.")
}

func addEngineAvailabilityLine(add func(Row), api API, hosts []*cluster.Host, hostsErr error, check, path, command string, minHosts int, protected string) {
	var current ct.PostgresAvailability
	if err := api.Get(path, &current); err != nil {
		add(Row{
			Status: "fail", Check: check, Detail: "error=" + err.Error(),
			Action: "Fix the controller, then rerun. This is not manual.",
		})
		return
	}
	switch current.Mode {
	case "manual":
		manualAction := "Run flynn-host " + command + " system to create platform replicas. Tenancy does not change this."
		if minHosts >= 3 {
			manualAction = "Run flynn-host " + command + " system to grow each resource when three hosts are registered. Tenancy does not change this."
		}
		add(Row{
			Status: "ok", Check: check, Detail: "mode=manual",
			Action: manualAction,
		})
	case "system":
		if hostsErr != nil {
			add(Row{
				Status: "warn", Check: check, Detail: "mode=system hosts=unknown",
				Action: "Fix the host list, then rerun. A warn does not exit 1.",
			})
			return
		}
		n := len(hosts)
		if n < minHosts {
			action := "Add a second host before a platform replica can leave the primary. This does not fail the cluster."
			if minHosts >= 3 {
				action = "Add hosts until three are registered. Failover stays unavailable below that. This does not fail the cluster."
			}
			add(Row{
				Status: "warn", Check: check,
				Detail: fmt.Sprintf("mode=system hosts=%d", n),
				Action: action,
			})
			return
		}
		okAction := "A primary is protected only while its platform replica is caught up. " + protected
		if minHosts >= 3 {
			okAction = "A resource is protected only while three members are live on three hosts. " + protected
		}
		add(Row{
			Status: "ok", Check: check,
			Detail: fmt.Sprintf("mode=system hosts=%d", n),
			Action: okAction,
		})
	default:
		add(Row{
			Status: "fail", Check: check, Detail: "error=unknown " + check,
			Action: "Set it: flynn-host " + command + " manual or system.",
		})
	}
}

const logSinkFixAction = "flynn-host log-sink:add syslog syslog+tls://logs.example:6514/ format rfc6587 and no prefix. A large cluster needs this sink. The ring is not retention."

func addLogSinkLine(add func(Row), api API, mode string) {
	var sinks []*ct.Sink
	if err := api.Get("/sinks", &sinks); err != nil {
		add(Row{
			Status: "fail",
			Check:  "log-sink",
			Detail: "error=" + err.Error(),
			Action: "Fix the controller, then rerun. This check does not call log-sink:list.",
		})
		return
	}
	if mode != tenancy.ModeHosted && mode != tenancy.ModeSelfHosted {
		add(Row{
			Status: "fail",
			Check:  "log-sink",
			Detail: "error=unknown tenancy",
			Action: "Tenancy is unread. This is not a self-hosted warning.",
		})
		return
	}
	detail, ok := logSinkCoverage(sinks)
	if ok {
		add(Row{
			Status: "ok",
			Check:  "log-sink",
			Detail: detail,
			Action: "log-sink:add remains the operator command.",
		})
		return
	}
	status := "fail"
	if mode == tenancy.ModeSelfHosted {
		status = "warn"
	}
	add(Row{
		Status: status,
		Check:  "log-sink",
		Detail: detail,
		Action: logSinkFixAction,
	})
}

// logSinkCoverage reports whether controller sinks cover system and app logs.
// A sink that does not parse is skipped. That is not a list-read error.
func logSinkCoverage(sinks []*ct.Sink) (string, bool) {
	all, system, apps := false, false, false
	for _, sink := range sinks {
		sys, app := qualifyingLogSink(sink)
		if sys && app {
			all = true
		}
		system = system || sys
		apps = apps || app
	}
	switch {
	case all:
		return "syslog+tls scope=all", true
	case system && apps:
		return "syslog+tls scope=system+apps", true
	case system:
		return "coverage=system", false
	case apps:
		return "coverage=apps", false
	default:
		return "coverage=none", false
	}
}

func qualifyingLogSink(sink *ct.Sink) (system, apps bool) {
	if sink == nil || sink.Kind != ct.SinkKindSyslog || strings.TrimSpace(sink.AppID) != "" || sink.Config == nil {
		return false, false
	}
	var cfg ct.SyslogSinkConfig
	if err := json.Unmarshal(*sink.Config, &cfg); err != nil {
		return false, false
	}
	if cfg.Insecure || strings.TrimSpace(cfg.Prefix) != "" {
		return false, false
	}
	u, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || u.Scheme != "syslog+tls" || u.Host == "" {
		return false, false
	}
	switch cfg.Format {
	case "", ct.SyslogFormatRFC6587:
	default:
		return false, false
	}
	scope, err := ct.ParseSinkScope(cfg.Scope)
	if err != nil {
		return false, false
	}
	switch scope {
	case ct.SinkScopeAll:
		return true, true
	case ct.SinkScopeSystem:
		return true, false
	case ct.SinkScopeApps:
		return false, true
	default:
		return false, false
	}
}

// serviceTokenApps are the plugin apps that carry a long-lived service token.
var serviceTokenApps = []struct{ app, env, scope string }{
	{"billing-plugin", "QUOTA_TOKEN", "account:quota"},
	{"letsencrypt-plugin", "ACME_TOKEN", "acme:issue"},
}

// addServiceTokenLine warns for each plugin service token that is expired,
// unparseable, carries the wrong scope, or is inside servicetoken.RenewBefore
// of expiry. Plugins that are not installed, or that hold no token, add no
// row. The token value is never printed.
func addServiceTokenLine(add func(Row), api API, now time.Time) {
	for _, st := range serviceTokenApps {
		release, err := api.GetAppRelease(st.app)
		if err != nil || release == nil {
			continue
		}
		tok := strings.TrimSpace(release.Env[st.env])
		if tok == "" {
			continue
		}
		if ok, why := servicetoken.Reusable(tok, st.scope, now); !ok {
			add(Row{
				Status: "warn", Check: "service-token",
				Detail: st.app + " " + st.env + " " + why,
				Action: "Run flynn-host token:rotate " + st.app + " " + st.env + " " + st.scope + ", or flynn plugin:update. Plugin update re-mints a token inside 30 days of expiry.",
			})
		}
	}
}

const placementCheckAction = "Run flynn-host placement:mode and flynn-host init --role. This check does not write the mode or any tag."

// PlacementJob is one running job sampled for the placement line.
// HostTags are the host's tags. Meta is the job metadata, which includes
// the app meta the scheduler copied.
type PlacementJob struct {
	HostTags map[string]string
	AppID    string
	AppName  string
	Type     string
	Meta     map[string]string
}

func addPlacementLine(add func(Row), api API, settings ct.WorkloadPlacement, readErr error, hosts []*cluster.Host, hostsErr error, jobs []PlacementJob, jobsErr error) {
	if readErr != nil {
		add(Row{Status: "fail", Check: "placement", Detail: "error=" + readErr.Error(), Action: placementCheckAction})
		return
	}
	mode := strings.TrimSpace(settings.Mode)
	if mode == "" {
		mode = placement.ModeMixed
	}
	if err := placement.ValidateMode(mode); err != nil {
		add(Row{Status: "fail", Check: "placement", Detail: "error=unknown placement mode", Action: placementCheckAction})
		return
	}
	if jobsErr != nil {
		add(Row{Status: "fail", Check: "placement", Detail: "error=" + jobsErr.Error(), Action: placementCheckAction})
		return
	}
	names, err := placementMismatches(api, jobs)
	if err != nil {
		add(Row{Status: "fail", Check: "placement", Detail: "error=" + err.Error(), Action: placementCheckAction})
		return
	}
	if mode == placement.ModeMixed {
		add(Row{Status: "ok", Check: "placement", Detail: "mode=mixed", Action: placementCheckAction})
		return
	}
	if hostsErr != nil {
		add(Row{Status: "fail", Check: "placement", Detail: "error=" + hostsErr.Error(), Action: placementCheckAction})
		return
	}
	core, app := placementRoleCounts(hosts)
	status := "ok"
	detail := fmt.Sprintf("mode=split core=%d app=%d", core, app)
	if core == 0 {
		status = "fail"
	} else if app == 0 {
		status = "warn"
	}
	if len(names) > 0 {
		if status != "fail" {
			status = "warn"
		}
		detail += " mismatch=" + strings.Join(names, ",")
	}
	add(Row{Status: status, Check: "placement", Detail: detail, Action: placementCheckAction})
}

func placementRoleCounts(hosts []*cluster.Host) (core, app int) {
	for _, h := range hosts {
		if h == nil || h.ShuttingDown() {
			continue
		}
		switch h.Tags()[placement.TagRole] {
		case placement.RoleCore:
			core++
		case placement.RoleApp:
			app++
		}
	}
	return core, app
}

// placementMismatches names apps whose non-omni jobs sit on the wrong role.
// The check uses split rules even while the cluster is mixed, then the caller
// drops the names so mixed does not warn. A release read error fails the line.
func placementMismatches(api API, jobs []PlacementJob) ([]string, error) {
	cache := map[string]*ct.Release{}
	seen := map[string]struct{}{}
	var names []string
	for _, job := range jobs {
		if job.AppID == "" {
			continue
		}
		rel, ok := cache[job.AppID]
		if !ok {
			var err error
			rel, err = api.GetAppRelease(job.AppID)
			if err != nil {
				return nil, err
			}
			cache[job.AppID] = rel
		}
		omni := false
		if rel != nil {
			if proc, ok := rel.Processes[job.Type]; ok {
				omni = proc.Omni
			}
		}
		app := &ct.App{ID: job.AppID, Name: job.AppName, Meta: map[string]string{}}
		for _, key := range []string{"flynn-system-app", "flynn-plugin", "flynn-datastore"} {
			if v, ok := job.Meta[key]; ok {
				app.Meta[key] = v
			}
		}
		if placement.HostAllows(placement.ModeSplit, placement.Classify(app, omni), job.HostTags) {
			continue
		}
		name := job.AppName
		if name == "" {
			name = job.AppID
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}
