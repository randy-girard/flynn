package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	ct "github.com/randy-girard/flynn/controller/types"
	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/joboutput"
	"golang.org/x/net/context"
)

const jobOutputLines = 10000

// archiveOutputSync stores deploy output before PutJob returns. Production
// leaves it false and archives in the background so a slow log aggregator
// cannot stall the scheduler.
func (c *controllerAPI) maybeArchiveJobOutput(job *ct.Job) {
	if c == nil || job == nil || job.UUID == "" || !job.IsDown() || !ct.ArchivesJobOutput(job.Type) {
		return
	}
	cp := *job
	if c.archiveOutputSync {
		c.archiveJobOutput(&cp)
		return
	}
	go c.archiveJobOutput(&cp)
}

func (c *controllerAPI) archiveJobOutput(job *ct.Job) {
	if c == nil || job == nil || c.logaggc == nil || c.jobRepo == nil {
		return
	}
	attempts := 5
	if c.archiveOutputSync {
		attempts = 1
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			time.Sleep(200 * time.Millisecond)
		}
		text, err := c.readJobOutput(job)
		if err != nil {
			last = err
			continue
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if err := c.jobRepo.SetOutput(job.UUID, text); err != nil {
			last = err
			continue
		}
		return
	}
	if last != nil {
		log15.Warn("could not store job output", "job.id", job.UUID, "err", last)
	}
}

func (c *controllerAPI) readJobOutput(job *ct.Job) (string, error) {
	ids := make([]string, 0, 2)
	if job.ID != "" {
		ids = append(ids, job.ID)
	}
	if job.UUID != "" && job.UUID != job.ID {
		ids = append(ids, job.UUID)
	}
	var best string
	var last error
	for _, id := range ids {
		text, err := c.readJobLog(job.AppID, id)
		if err != nil {
			last = err
			continue
		}
		if len(text) > len(best) {
			best = text
		}
	}
	if best != "" {
		return best, nil
	}
	return "", last
}

func (c *controllerAPI) readJobLog(appID, jobID string) (string, error) {
	lines := jobOutputLines
	rc, err := c.logaggc.GetLog(appID, &logagg.LogOpts{
		JobID: jobID,
		Lines: &lines,
		StreamTypes: []logagg.StreamType{
			logagg.StreamTypeStdout,
			logagg.StreamTypeStderr,
		},
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var b strings.Builder
	dec := json.NewDecoder(rc)
	for {
		var msg logaggc.Message
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		if msg.Stream != "" && msg.Stream != logagg.StreamTypeStdout && msg.Stream != logagg.StreamTypeStderr {
			continue
		}
		if msg.Msg == "" {
			continue
		}
		b.WriteString(msg.Msg)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func (c *controllerAPI) GetJobOutput(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	params, _ := ctxhelper.ParamsFromContext(ctx)
	job, err := c.jobRepo.GetInApp(app.ID, params.ByName("jobs_id"))
	if err != nil {
		respondWithError(w, err)
		return
	}
	text, err := c.jobRepo.Output(job.UUID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	writePlainOutput(w, c.redactOutput(ctx, app, text, job.ReleaseID))
}

func (c *controllerAPI) GetReleaseOutput(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	rel, err := c.releaseForApp(ctx, app)
	if err != nil {
		respondWithError(w, err)
		return
	}
	text, err := c.releaseRepo.Output(rel.ID)
	if err != nil {
		respondWithError(w, err)
		return
	}
	writePlainOutput(w, c.redactOutput(ctx, app, text, rel.ID))
}

func (c *controllerAPI) PutReleaseOutput(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	app := c.getApp(ctx)
	rel, err := c.releaseForApp(ctx, app)
	if err != nil {
		respondWithError(w, err)
		return
	}
	text, err := readOutputBody(req.Body)
	if err != nil {
		respondWithError(w, err)
		return
	}
	if err := c.releaseRepo.SetOutput(rel.ID, text); err != nil {
		respondWithError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (c *controllerAPI) releaseForApp(ctx context.Context, app *ct.App) (*ct.Release, error) {
	rel, err := c.getRelease(ctx)
	if err != nil {
		return nil, err
	}
	if app == nil || rel.AppID != app.ID {
		return nil, ErrNotFound
	}
	return rel, nil
}

func readOutputBody(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, int64(joboutput.MaxBytes)*2+1))
	if err != nil {
		return "", err
	}
	return joboutput.Tail(string(b), joboutput.MaxBytes), nil
}

func writePlainOutput(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, text)
}

// redactOutput applies the same env redaction as the live log. Callers who
// can read release env, including the cluster key, get the stored text unchanged.
func (c *controllerAPI) redactOutput(ctx context.Context, app *ct.App, text string, releaseIDs ...string) string {
	if text == "" || c == nil || mayReadReleaseEnv(ctx, app) {
		return text
	}
	seen := make(map[string]struct{})
	var secrets []string
	add := func(rel *ct.Release) {
		for _, secret := range releaseLogSecrets(rel) {
			if _, ok := seen[secret]; ok {
				continue
			}
			seen[secret] = struct{}{}
			secrets = append(secrets, secret)
		}
	}
	for _, id := range releaseIDs {
		if id == "" || c.releaseRepo == nil {
			continue
		}
		data, err := c.releaseRepo.Get(id)
		if err != nil {
			continue
		}
		add(data.(*ct.Release))
	}
	if c.appRepo != nil && app != nil {
		if rel, err := c.appRepo.GetRelease(app.ID); err == nil {
			add(rel)
		}
	}
	sort.Slice(secrets, func(i, j int) bool {
		if len(secrets[i]) != len(secrets[j]) {
			return len(secrets[i]) > len(secrets[j])
		}
		return secrets[i] < secrets[j]
	})
	rep := logSecretReplacer(secrets)
	if rep == nil {
		return text
	}
	return rep.Replace(text)
}
