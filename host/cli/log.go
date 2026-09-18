package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/flynn/go-docopt"
	"github.com/randy-girard/flynn/host/types"
	"github.com/randy-girard/flynn/pkg/cluster"
)

func init() {
	Register("log", runLog, `
usage: flynn-host log [--init] [-f|--follow] [--lines=<number>] [--split-stderr] [-a|--all] ID

Get logs of a job, or of every job for an app.

ID is a job ID (from flynn-host ps) or a controller app name (for example
dashboard or postgres). When more than one job matches, each line is prefixed
with app.type.job.

Options:
  -f, --follow         stream new lines
  --lines=<number>     show only the last n lines
  --split-stderr       send stderr to stderr
  --init               include containerinit logs
  -a, --all            include jobs that are not running (app name only)
`)
}

func runLog(args *docopt.Args, client *cluster.Client) error {
	name := args.String["ID"]
	follow := args.Bool["-f"] || args.Bool["--follow"]
	all := args.Bool["-a"] || args.Bool["--all"]

	lines := 0
	if args.String["--lines"] != "" {
		var err error
		lines, err = strconv.Atoi(args.String["--lines"])
		if err != nil {
			return err
		}
	}

	stderr := os.Stdout
	if args.Bool["--split-stderr"] {
		stderr = os.Stderr
	}

	jobs, err := lookupLogJobs(client, name, all)
	if err != nil {
		return err
	}
	if follow {
		jobs = runningLogJobs(jobs)
		if len(jobs) == 0 {
			return fmt.Errorf("no running jobs for %q", name)
		}
	}

	prefix := len(jobs) > 1
	var mu sync.Mutex
	streamOne := func(job host.ActiveJob) error {
		stdoutW, stderrW := io.Writer(os.Stdout), io.Writer(stderr)
		if prefix {
			p := logLinePrefix(job)
			stdoutW = &prefixWriter{w: os.Stdout, prefix: p, atBOL: true, mu: &mu}
			stderrW = &prefixWriter{w: stderr, prefix: p, atBOL: true, mu: &mu}
		}
		if lines > 0 {
			stdoutR, stdoutPW := io.Pipe()
			stderrR, stderrPW := io.Pipe()
			go func() {
				_ = getLog(jobHostID(job), job.Job.ID, client, false, args.Bool["--init"], stdoutPW, stderrPW)
				stdoutPW.Close()
				stderrPW.Close()
			}()
			tailLogs(stdoutR, stderrR, lines, stdoutW, stderrW)
			return nil
		}
		return getLog(
			jobHostID(job),
			job.Job.ID,
			client,
			follow,
			args.Bool["--init"],
			stdoutW,
			stderrW,
		)
	}

	if !follow || len(jobs) == 1 {
		var first error
		for _, job := range jobs {
			if err := streamOne(job); err != nil && first == nil {
				first = err
			}
		}
		return first
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(jobs))
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := streamOne(job); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func jobHostID(job host.ActiveJob) string {
	if job.Job == nil {
		return ""
	}
	hostID, err := cluster.ExtractHostID(job.Job.ID)
	if err != nil {
		return ""
	}
	return hostID
}

func lookupLogJobs(client *cluster.Client, name string, all bool) (sortJobs, error) {
	if hostID, err := cluster.ExtractHostID(name); err == nil {
		if hc, err := client.Host(hostID); err == nil {
			if job, err := hc.GetJob(name); err == nil && job != nil && job.Job != nil {
				return sortJobs{*job}, nil
			}
		}
	}
	jobs, err := jobList(client, all)
	if err != nil {
		return nil, err
	}
	matched := jobsMatchingApp(jobs, name)
	if len(matched) == 0 {
		if all {
			return nil, fmt.Errorf("no jobs found for %q (not a job ID or app name)", name)
		}
		return nil, fmt.Errorf("no running jobs for %q (try flynn-host log --all %s)", name, name)
	}
	return matched, nil
}

func runningLogJobs(jobs sortJobs) sortJobs {
	var out sortJobs
	for _, job := range jobs {
		if job.Status == host.StatusStarting || job.Status == host.StatusRunning {
			out = append(out, job)
		}
	}
	return out
}

func jobsMatchingApp(jobs []host.ActiveJob, name string) sortJobs {
	var out sortJobs
	for _, job := range jobs {
		if jobMatchesApp(job, name) {
			out = append(out, job)
		}
	}
	return out
}

func jobMatchesApp(job host.ActiveJob, name string) bool {
	if name == "" || job.Job == nil {
		return false
	}
	meta := job.Job.Metadata
	return meta["flynn-controller.app_name"] == name || meta["flynn-controller.app"] == name
}

func logLinePrefix(job host.ActiveJob) string {
	id := ""
	app, ptype := "", ""
	if job.Job != nil {
		id = job.Job.ID
		if job.Job.Metadata != nil {
			app = job.Job.Metadata["flynn-controller.app_name"]
			ptype = job.Job.Metadata["flynn-controller.type"]
		}
		if u, err := cluster.ExtractUUID(id); err == nil && len(u) >= 8 {
			id = u[:8]
		}
	}
	switch {
	case app != "" && ptype != "":
		return app + "." + ptype + "." + id + " | "
	case app != "":
		return app + "." + id + " | "
	default:
		return id + " | "
	}
}

type prefixWriter struct {
	w      io.Writer
	prefix string
	atBOL  bool
	mu     *sync.Mutex
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	if p.mu != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
	}
	if len(b) == 0 {
		return 0, nil
	}
	out := make([]byte, 0, len(b)+len(p.prefix))
	for _, c := range b {
		if p.atBOL {
			out = append(out, p.prefix...)
			p.atBOL = false
		}
		out = append(out, c)
		if c == '\n' {
			p.atBOL = true
		}
	}
	if _, err := p.w.Write(out); err != nil {
		return 0, err
	}
	return len(b), nil
}

func getLog(hostID, jobID string, client *cluster.Client, follow, init bool, stdout, stderr io.Writer) error {
	hostClient, err := client.Host(hostID)
	if err != nil {
		return fmt.Errorf("could not connect to host %s: %s", hostID, err)
	}
	attachReq := &host.AttachReq{
		JobID: jobID,
		Flags: host.AttachFlagStdout | host.AttachFlagStderr | host.AttachFlagLogs,
	}
	if follow {
		attachReq.Flags |= host.AttachFlagStream
	}
	if init {
		attachReq.Flags |= host.AttachFlagInitLog
	}
	attachClient, err := hostClient.Attach(attachReq, false)
	if err != nil {
		switch err {
		case host.ErrJobNotRunning, host.ErrAttached:
			return nil
		case cluster.ErrWouldWait:
			return errors.New("no such job")
		}
		return err
	}
	defer attachClient.Close()
	_, err = attachClient.Receive(stdout, stderr)
	return err
}

type LogLine struct {
	Token   int
	Content []byte
}

type LogRing struct {
	logLines []*LogLine
	start    int
}

func NewLogRing(capacity int) *LogRing {
	return &LogRing{
		logLines: make([]*LogLine, 0, capacity),
	}
}

func (r *LogRing) Add(l *LogLine) {
	if len(r.logLines) < cap(r.logLines) {
		r.logLines = append(r.logLines, l)
	} else {
		r.logLines[r.start] = l
		r.start++

		if r.start == cap(r.logLines) {
			r.start = 0
		}
	}
}

func (r *LogRing) Read() []*LogLine {
	buf := make([]*LogLine, len(r.logLines))
	if n := copy(buf, r.logLines[r.start:len(r.logLines)]); n < len(r.logLines) {
		copy(buf[n:], r.logLines[:r.start])
	}
	return buf
}

func scanLogs(reader io.Reader, token int, output chan LogLine) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		buf := make([]byte, len(scanner.Bytes())+1)
		copy(buf, scanner.Bytes())
		buf[len(buf)-1] = '\n'
		output <- LogLine{Token: token, Content: buf}
	}
	close(output)
}

func tailLogs(stdoutR, stderrR io.Reader, lines int, stdoutW, stderrW io.Writer) {
	gather1 := make(chan LogLine)
	gather2 := make(chan LogLine)
	r := NewLogRing(lines)
	go scanLogs(stdoutR, 0, gather1)
	go scanLogs(stderrR, 1, gather2)
	for {
		select {
		case v, ok := <-gather1:
			if ok {
				r.Add(&v)
			} else {
				gather1 = nil
			}
		case v, ok := <-gather2:
			if ok {
				r.Add(&v)
			} else {
				gather2 = nil
			}
		}
		if gather1 == nil && gather2 == nil {
			break
		}
	}

	for _, ll := range r.Read() {
		switch ll.Token {
		case 0:
			stdoutW.Write(ll.Content)
		case 1:
			stderrW.Write(ll.Content)
		}
	}
}
