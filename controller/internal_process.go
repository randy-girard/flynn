package main

import (
	"bufio"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/randy-girard/flynn/controller/authz"
	ct "github.com/randy-girard/flynn/controller/types"
	"golang.org/x/net/context"
)

func hideInternal(ctx context.Context, app *ct.App) bool {
	system := app != nil && app.System()
	return authz.HideInternalProcesses(authz.TokenFromContext(ctx), system)
}

func hideInternalToken(ctx context.Context) bool {
	return authz.HideInternalProcesses(authz.TokenFromContext(ctx), false)
}

func hideInternalLimits(ctx context.Context, app *ct.App) bool {
	return hideInternal(ctx, app)
}

func preserveInternalProcessTypes(dst map[string]ct.ProcessType, prev *ct.Release) map[string]ct.ProcessType {
	if dst == nil {
		dst = make(map[string]ct.ProcessType)
	}
	for k := range dst {
		if ct.IsInternalProcessType(k) {
			delete(dst, k)
		}
	}
	if prev == nil {
		return dst
	}
	for k, v := range prev.Processes {
		if ct.IsInternalProcessType(k) {
			dst[k] = v
		}
	}
	return dst
}

const metaSystemApp = "flynn-system-app"

func stripSystemAppMetaUnlessAdmin(ctx context.Context, meta map[string]string) {
	if authz.SystemAppAllowed(authz.TokenFromContext(ctx), true) {
		return
	}
	delete(meta, metaSystemApp)
}

func sanitizeAppMetaUpdate(ctx context.Context, existing *ct.App, meta interface{}) interface{} {
	tok := authz.TokenFromContext(ctx)
	if authz.SystemAppAllowed(tok, true) {
		return meta
	}
	m := metaToStringMap(meta)
	if m == nil {
		if existing != nil && existing.System() {
			return map[string]string{metaSystemApp: "true"}
		}
		return meta
	}
	delete(m, metaSystemApp)
	if existing != nil && existing.System() {
		m[metaSystemApp] = "true"
	}
	return m
}

func metaToStringMap(v interface{}) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		for k, val := range m {
			out[k] = val
		}
		return out
	case map[string]interface{}:
		out := make(map[string]string, len(m))
		for k, val := range m {
			s, ok := val.(string)
			if !ok {
				return nil
			}
			out[k] = s
		}
		return out
	default:
		return nil
	}
}

func processTypesPrivileged(procs map[string]ct.ProcessType) bool {
	for _, p := range procs {
		if p.HostNetwork || p.HostPIDNamespace || p.WriteableCgroups ||
			len(p.LinuxCapabilities) > 0 || len(p.AllowedDevices) > 0 ||
			len(p.Mounts) > 0 || len(p.Profiles) > 0 {
			return true
		}
	}
	return false
}

func stripPrivilegedProcessTypes(procs map[string]ct.ProcessType) map[string]ct.ProcessType {
	if procs == nil {
		return nil
	}
	out := make(map[string]ct.ProcessType, len(procs))
	for name, p := range procs {
		p.HostNetwork = false
		p.HostPIDNamespace = false
		p.WriteableCgroups = false
		p.LinuxCapabilities = nil
		p.AllowedDevices = nil
		p.Mounts = nil
		p.Profiles = nil
		out[name] = p
	}
	return out
}

// hideProcessFromUser is slugrunner. slugbuilder and dockerbuilder stay
// visible: their stdout is the git-push log the dashboard shows.
func hideProcessFromUser(name string) bool {
	return ct.IsInternalProcessType(name) && !ct.IsBuilderProcessType(name)
}

func redactJobs(jobs []*ct.Job) []*ct.Job {
	if len(jobs) == 0 {
		return jobs
	}
	out := make([]*ct.Job, 0, len(jobs))
	for _, j := range jobs {
		if j == nil || hideProcessFromUser(j.Type) {
			continue
		}
		out = append(out, j)
	}
	return out
}

func (c *controllerAPI) annotateAppBuilding(app *ct.App) {
	if app == nil {
		return
	}
	now := time.Now()
	if c == nil || c.jobRepo == nil || app.ID == "" {
		app.Building = ct.AppMetaIsBuilding(app.Meta, now)
		return
	}
	list, err := c.jobRepo.List(app.ID)
	if err != nil {
		app.Building = ct.AppMetaIsBuilding(app.Meta, now)
		return
	}
	app.Building = ct.AppBuildingShown(app.Meta, list, now)
}

// presentRelease is the release returned to an HTTP caller. Builder process
// types stay stripped for non-controller-key callers. Release env and
// per-process env are omitted unless the caller has app:env:read (or a grant
// that expands to it, or cluster-admin).
func presentRelease(ctx context.Context, app *ct.App, r *ct.Release) *ct.Release {
	if r == nil {
		return nil
	}
	if hideInternalLimits(ctx, app) {
		r = redactRelease(r)
	}
	if !mayReadReleaseEnv(ctx, app) {
		r = redactReleaseEnv(r)
	}
	return r
}

func mayReadReleaseEnv(ctx context.Context, app *ct.App) bool {
	id, name := "", ""
	if app != nil {
		id, name = app.ID, app.Name
	}
	return authz.MayReadAppEnv(authz.TokenFromContext(ctx), id, name)
}

// redactReleaseEnv copies r with release env and per-process env removed.
// Process commands, artifacts, and other metadata stay. The stored release
// is not modified.
func redactReleaseEnv(r *ct.Release) *ct.Release {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Env = nil
	if r.Processes == nil {
		return &cp
	}
	procs := make(map[string]ct.ProcessType, len(r.Processes))
	for k, v := range r.Processes {
		v.Env = nil
		procs[k] = v
	}
	cp.Processes = procs
	return &cp
}

func presentExpandedFormation(ctx context.Context, ef *ct.ExpandedFormation) *ct.ExpandedFormation {
	if ef == nil {
		return nil
	}
	app := ef.App
	if hideInternal(ctx, app) && (app == nil || !app.System()) {
		ef = redactExpandedFormation(ef)
	}
	if ef == nil || ef.Release == nil || mayReadReleaseEnv(ctx, app) {
		return ef
	}
	cp := *ef
	cp.Release = redactReleaseEnv(ef.Release)
	return &cp
}

func presentExpandedFormations(ctx context.Context, list []*ct.ExpandedFormation) []*ct.ExpandedFormation {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.ExpandedFormation, len(list))
	for i, ef := range list {
		out[i] = presentExpandedFormation(ctx, ef)
	}
	return out
}

func presentEvent(ctx context.Context, app *ct.App, e *ct.Event) *ct.Event {
	if e == nil {
		return nil
	}
	hide := hideInternalToken(ctx)
	if app != nil {
		hide = hideInternal(ctx, app)
	}
	if hide {
		e = redactEvent(e)
	}
	if !mayExportRouteKey(ctx) {
		e = omitEventRouteKey(e)
	}
	if e == nil || e.ObjectType != ct.EventTypeRelease {
		return e
	}
	evApp := app
	if evApp == nil || (e.AppID != "" && evApp.ID != e.AppID) {
		evApp = &ct.App{ID: e.AppID}
	}
	if mayReadReleaseEnv(ctx, evApp) {
		return e
	}
	return redactEventReleaseEnv(e)
}

func presentEvents(ctx context.Context, app *ct.App, list []*ct.Event) []*ct.Event {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.Event, 0, len(list))
	for _, e := range list {
		if hideInternal(ctx, app) || (app == nil && hideInternalToken(ctx)) {
			if dropInternalEvent(e) {
				continue
			}
		}
		out = append(out, presentEvent(ctx, app, e))
	}
	return out
}

func redactEventReleaseEnv(e *ct.Event) *ct.Event {
	if e == nil || e.ObjectType != ct.EventTypeRelease {
		return e
	}
	var rel ct.Release
	if json.Unmarshal(e.Data, &rel) != nil {
		return e
	}
	b, err := json.Marshal(redactReleaseEnv(&rel))
	if err != nil {
		return e
	}
	cp := *e
	cp.Data = b
	return &cp
}

func redactRelease(r *ct.Release) *ct.Release {
	if r == nil || r.Processes == nil {
		return r
	}
	cp := *r
	cp.Processes = redactProcessTypes(r.Processes)
	return &cp
}

func presentReleases(ctx context.Context, app *ct.App, list []*ct.Release) []*ct.Release {
	list = ct.WithoutBuildPlaceholders(list)
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.Release, len(list))
	for i, r := range list {
		out[i] = presentRelease(ctx, app, r)
	}
	return out
}

func redactReleases(list []*ct.Release) []*ct.Release {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.Release, len(list))
	for i, r := range list {
		out[i] = redactRelease(r)
	}
	return out
}

func redactProcessTypes(in map[string]ct.ProcessType) map[string]ct.ProcessType {
	if in == nil {
		return nil
	}
	out := make(map[string]ct.ProcessType, len(in))
	for k, v := range in {
		if ct.IsInternalProcessType(k) {
			continue
		}
		out[k] = v
	}
	return out
}

func redactProcessCounts(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		if ct.IsInternalProcessType(k) {
			continue
		}
		out[k] = v
	}
	return out
}

func redactProcessTags(in map[string]map[string]string) map[string]map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(in))
	for k, v := range in {
		if ct.IsInternalProcessType(k) {
			continue
		}
		out[k] = v
	}
	return out
}

func redactFormation(f *ct.Formation) *ct.Formation {
	if f == nil {
		return f
	}
	cp := *f
	cp.Processes = redactProcessCounts(f.Processes)
	cp.Tags = redactProcessTags(f.Tags)
	return &cp
}

func redactFormations(list []*ct.Formation) []*ct.Formation {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.Formation, len(list))
	for i, f := range list {
		out[i] = redactFormation(f)
	}
	return out
}

func redactScaleRequest(sr *ct.ScaleRequest) *ct.ScaleRequest {
	if sr == nil {
		return nil
	}
	cp := *sr
	cp.OldProcesses = redactProcessCounts(sr.OldProcesses)
	if sr.NewProcesses != nil {
		np := redactProcessCounts(*sr.NewProcesses)
		cp.NewProcesses = &np
	}
	cp.OldTags = redactProcessTags(sr.OldTags)
	if sr.NewTags != nil {
		nt := redactProcessTags(*sr.NewTags)
		cp.NewTags = &nt
	}
	return &cp
}

func redactExpandedFormation(ef *ct.ExpandedFormation) *ct.ExpandedFormation {
	if ef == nil {
		return nil
	}
	out := *ef
	out.Release = redactRelease(ef.Release)
	out.Processes = redactProcessCounts(ef.Processes)
	out.Tags = redactProcessTags(ef.Tags)
	out.PendingScaleRequest = redactScaleRequest(ef.PendingScaleRequest)
	return &out
}

func redactExpandedFormations(list []*ct.ExpandedFormation) []*ct.ExpandedFormation {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.ExpandedFormation, len(list))
	for i, ef := range list {
		if ef != nil && ef.App != nil && ef.App.System() {
			out[i] = ef
			continue
		}
		out[i] = redactExpandedFormation(ef)
	}
	return out
}

func preserveInternalProcessCounts(dst, src map[string]int) map[string]int {
	if dst == nil {
		dst = make(map[string]int)
	}
	for k, v := range src {
		if ct.IsInternalProcessType(k) {
			dst[k] = v
		}
	}
	return dst
}

func stripInternalProcessCounts(in map[string]int) {
	for k := range in {
		if ct.IsInternalProcessType(k) {
			delete(in, k)
		}
	}
}

// lockInternalProcessCounts drops attempted slugbuilder/dockerbuilder/slugrunner
// scale values and restores whatever the existing formation already had.
func lockInternalProcessCounts(existing, incoming map[string]int) map[string]int {
	if incoming == nil {
		return incoming
	}
	stripInternalProcessCounts(incoming)
	return preserveInternalProcessCounts(incoming, existing)
}

// minLogSecretLen skips short release env values ("true", "1", "web") so
// log redaction does not punch holes in ordinary log text. Longer values
// are treated as secrets when the caller cannot read app env.
const minLogSecretLen = 8

const logRedacted = "[redacted]"

// releaseLogSecrets is the current release and per-process env values that
// must not appear in log lines for a caller without app:env:read.
// Longer values come first so a short secret cannot eat a longer one.
func releaseLogSecrets(rel *ct.Release) []string {
	if rel == nil {
		return nil
	}
	seen := make(map[string]struct{})
	add := func(v string) {
		if len(strings.TrimSpace(v)) < minLogSecretLen {
			return
		}
		seen[v] = struct{}{}
	}
	for _, v := range rel.Env {
		add(v)
	}
	for _, p := range rel.Processes {
		for _, v := range p.Env {
			add(v)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// logSecretReplacer is built once per log request. It replaces each secret
// and, when encoding/json would escape it inside a log line, that escaped
// form too.
func logSecretReplacer(secrets []string) *strings.Replacer {
	pairs := make([]string, 0, len(secrets)*4)
	seen := make(map[string]struct{}, len(secrets))
	add := func(old string) {
		if old == "" {
			return
		}
		if _, ok := seen[old]; ok {
			return
		}
		seen[old] = struct{}{}
		pairs = append(pairs, old, logRedacted)
	}
	for _, secret := range secrets {
		add(secret)
		if esc := jsonStringContents(secret); esc != secret {
			add(esc)
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	return strings.NewReplacer(pairs...)
}

func jsonStringContents(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

func redactLogLine(line []byte, secrets []string) []byte {
	rep := logSecretReplacer(secrets)
	if rep == nil {
		return append([]byte(nil), line...)
	}
	return []byte(rep.Replace(string(line)))
}

// redactLogSecrets filters an NDJSON log stream. The replacer is built once
// for the request; each line is copied so the scanner buffer can be reused.
func redactLogSecrets(r io.ReadCloser, secrets []string) io.ReadCloser {
	rep := logSecretReplacer(secrets)
	if rep == nil {
		return r
	}
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var err error
		for sc.Scan() {
			line := rep.Replace(sc.Text())
			if _, err = pw.Write([]byte(line)); err != nil {
				break
			}
			if _, err = pw.Write([]byte("\n")); err != nil {
				break
			}
		}
		if err == nil {
			err = sc.Err()
		}
		pw.CloseWithError(err)
	}()
	return pr
}

func logLineInternalProcess(raw []byte) bool {
	var m struct {
		ProcessType string `json:"process_type"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return hideProcessFromUser(m.ProcessType)
}

func filterInternalProcessLogs(r io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		// NDJSON (one object per line). A streaming json.Decoder can block
		// forever on a follow stream that has not closed a token.
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var err error
		for sc.Scan() {
			raw := sc.Bytes()
			if logLineInternalProcess(raw) {
				continue
			}
			if _, err = pw.Write(raw); err != nil {
				break
			}
			if _, err = pw.Write([]byte("\n")); err != nil {
				break
			}
		}
		if err == nil {
			err = sc.Err()
		}
		pw.CloseWithError(err)
	}()
	return pr
}

func dropInternalEvent(e *ct.Event) bool {
	if e == nil || e.ObjectType != ct.EventTypeJob {
		return false
	}
	var job ct.Job
	if json.Unmarshal(e.Data, &job) != nil {
		return false
	}
	return ct.IsInternalProcessType(job.Type)
}

func redactEvent(e *ct.Event) *ct.Event {
	if e == nil {
		return nil
	}
	switch e.ObjectType {
	case ct.EventTypeScaleRequest, ct.EventTypeScaleRequestCancelation:
		var req ct.ScaleRequest
		if json.Unmarshal(e.Data, &req) != nil {
			return e
		}
		b, err := json.Marshal(redactScaleRequest(&req))
		if err != nil {
			return e
		}
		cp := *e
		cp.Data = b
		return &cp
	case ct.EventTypeRelease:
		var rel ct.Release
		if json.Unmarshal(e.Data, &rel) != nil {
			return e
		}
		b, err := json.Marshal(redactRelease(&rel))
		if err != nil {
			return e
		}
		cp := *e
		cp.Data = b
		return &cp
	case ct.EventTypeDeprecatedScale:
		var sc ct.DeprecatedScale
		if json.Unmarshal(e.Data, &sc) != nil {
			return e
		}
		sc.PrevProcesses = redactProcessCounts(sc.PrevProcesses)
		sc.Processes = redactProcessCounts(sc.Processes)
		b, err := json.Marshal(sc)
		if err != nil {
			return e
		}
		cp := *e
		cp.Data = b
		return &cp
	default:
		return e
	}
}

func redactEvents(list []*ct.Event) []*ct.Event {
	if len(list) == 0 {
		return list
	}
	out := make([]*ct.Event, 0, len(list))
	for _, e := range list {
		if dropInternalEvent(e) {
			continue
		}
		out = append(out, redactEvent(e))
	}
	return out
}
