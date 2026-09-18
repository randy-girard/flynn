package main

import (
	"encoding/json"
	"io"

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
	if app != nil && app.System() {
		return false
	}
	tok := authz.TokenFromContext(ctx)
	if app == nil {
		return !authz.CanManageInternalProcessLimits(tok, "")
	}
	return !authz.CanManageInternalProcessLimits(tok, app.ID) && !authz.CanManageInternalProcessLimits(tok, app.Name)
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

func redactJobs(jobs []*ct.Job) []*ct.Job {
	if len(jobs) == 0 {
		return jobs
	}
	out := make([]*ct.Job, 0, len(jobs))
	for _, j := range jobs {
		if j == nil || ct.IsInternalProcessType(j.Type) {
			continue
		}
		out = append(out, j)
	}
	return out
}

func redactRelease(r *ct.Release) *ct.Release {
	if r == nil || r.Processes == nil {
		return r
	}
	cp := *r
	cp.Processes = redactProcessTypes(r.Processes)
	return &cp
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

func logLineInternalProcess(raw []byte) bool {
	var m struct {
		ProcessType string `json:"process_type"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return false
	}
	return ct.IsInternalProcessType(m.ProcessType)
}

func filterInternalProcessLogs(r io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		defer r.Close()
		dec := json.NewDecoder(r)
		var err error
		for {
			var raw json.RawMessage
			if err = dec.Decode(&raw); err != nil {
				if err == io.EOF {
					err = nil
				}
				break
			}
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
