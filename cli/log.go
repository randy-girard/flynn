package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strconv"

	"github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	logaggc "github.com/randy-girard/flynn/logaggregator/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/cluster"

	"github.com/flynn/go-docopt"
)

func init() {
	register("log", runLog, `
usage: flynn log [-f] [-j <id>] [-n <lines>] [-r] [-s] [-t <type>] [-i]

Stream log for an app.

Lines are prefixed with source[name], using the short process name when
allocated (app[web.1], flynn[web.4821]). Filter with -j using that name or
the job UUID; filtering always uses the host job id, not the display name.

Options:
	-f, --follow               stream new lines
	-j, --job=<id>             filter logs to a job name (web.4821) or UUID
	-n, --number=<lines>       return at most n lines from the log buffer
	-r, --raw-output           output raw log messages with no prefix
	-s, --split-stderr         send stderr lines to stderr
	-t, --process-type=<type>  filter logs to a specific process type
	-i, --init                 output containerinit logs to stderr
`)
}

// like time.RFC3339Nano except it only goes to 6 decimals and doesn't drop
// trailing zeros
const rfc3339micro = "2006-01-02T15:04:05.000000Z07:00"

func runLog(args *docopt.Args, client controller.Client) error {
	rawOutput := args.Bool["--raw-output"]
	jobID := args.String["--job"]
	if jobID != "" && ct.IsJobName(jobID) {
		if j, err := client.GetJob(mustApp(), jobID); err == nil {
			if j.ID != "" {
				jobID = j.ID
			} else {
				jobID = j.UUID
			}
		}
	}
	opts := logagg.LogOpts{
		Follow:      args.Bool["--follow"],
		JobID:       jobID,
		StreamTypes: logagg.DefaultStreamTypes(),
	}
	if ptype, ok := args.String["--process-type"]; ok {
		opts.ProcessType = &ptype
	}
	if strlines := args.String["--number"]; strlines != "" {
		lines, err := strconv.Atoi(strlines)
		if err != nil {
			return err
		}
		opts.Lines = &lines
	}
	if args.Bool["--init"] {
		opts.StreamTypes = append(opts.StreamTypes, logagg.StreamTypeInit)
	}

	names := jobNameIndex(nil)
	if !rawOutput {
		if jobs, err := client.JobList(mustApp()); err == nil {
			names = jobNameIndex(jobs)
		}
	}

	rc, err := client.GetAppLog(mustApp(), &opts)
	if err != nil {
		return err
	}
	defer rc.Close()

	var stderr io.Writer = os.Stdout
	if args.Bool["--split-stderr"] {
		stderr = os.Stderr
	}
	var initOut io.Writer = ioutil.Discard
	if args.Bool["--init"] {
		initOut = os.Stderr
	}

	dec := json.NewDecoder(rc)
	for {
		var msg logaggc.Message
		err := dec.Decode(&msg)
		if err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}

		var stream io.Writer
		switch msg.Stream {
		case logagg.StreamTypeStdout, logagg.StreamTypeSystem:
			stream = os.Stdout
		case logagg.StreamTypeStderr:
			stream = stderr
		case logagg.StreamTypeInit:
			stream = initOut
		default:
			continue
		}
		if rawOutput {
			fmt.Fprintln(stream, msg.Msg)
		} else {
			fmt.Fprintln(stream, formatLogLine(msg, names))
		}
	}
}

func formatLogLine(msg logaggc.Message, names map[string]string) string {
	tstamp := msg.Timestamp.Format(rfc3339micro)
	return fmt.Sprintf("%s %s[%s]: %s", tstamp, msg.Source, logJobLabel(msg, names), msg.Msg)
}

func logJobLabel(msg logaggc.Message, names map[string]string) string {
	if msg.JobName != "" {
		return msg.JobName
	}
	if n := lookupJobName(names, msg.JobID); n != "" {
		return n
	}
	if msg.ProcessType != "" {
		return msg.ProcessType + "." + msg.JobID
	}
	return msg.JobID
}

func jobNameIndex(jobs []*ct.Job) map[string]string {
	m := make(map[string]string)
	for _, j := range jobs {
		if j == nil {
			continue
		}
		name := ct.JobDisplayName(j)
		if name == "" {
			continue
		}
		if j.ID != "" {
			m[j.ID] = name
		}
		if j.UUID != "" {
			m[j.UUID] = name
		}
	}
	return m
}

func lookupJobName(names map[string]string, jobID string) string {
	if len(names) == 0 || jobID == "" {
		return ""
	}
	if n := names[jobID]; n != "" {
		return n
	}
	if u, err := cluster.ExtractUUID(jobID); err == nil {
		if n := names[u]; n != "" {
			return n
		}
	}
	return ""
}
