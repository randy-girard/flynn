package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/randy-girard/flynn/logaggregator/client"
	"github.com/randy-girard/flynn/logaggregator/snapshot"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/appliancehttp"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/status"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"

	"github.com/inconshreveable/log15"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/net/context"
)

// apiHandler serves the log API. Buffered logs of every app (and the host
// cursors) are behind the cluster key; only the health check is public.
func apiHandler(agg *Aggregator, cursors *HostCursors, keys []string, snaps *snapshotTracker) http.Handler {
	api := aggregatorAPI{agg, cursors, snaps}
	r := httprouter.New()

	r.Handler("GET", status.Path, status.HealthyHandler)
	r.GET("/log/:channel_id", httphelper.WrapHandler(api.GetLog))
	r.GET("/cursors", httphelper.WrapHandler(api.GetCursors))
	r.GET("/snapshot", httphelper.WrapHandler(api.GetSnapshot))
	return httphelper.ContextInjector(
		"logaggregator-api",
		httphelper.NewRequestLogger(appliancehttp.RequireKeys(keys, r)),
	)
}

type aggregatorAPI struct {
	agg     *Aggregator
	cursors *HostCursors
	snaps   *snapshotTracker
}

// snapshotTracker counts the snapshot responses being written, so a
// stopping instance can let them finish (Server.Shutdown).
type snapshotTracker struct {
	wg sync.WaitGroup

	mtx sync.Mutex
	// last is when the last snapshot response finished.
	last time.Time

	// hook, when set (tests), runs after the snapshot is taken and before
	// it is written.
	hook func()
}

func (a *aggregatorAPI) GetCursors(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	httphelper.JSON(w, 200, a.cursors.Get())
}

func (a *aggregatorAPI) GetLog(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(ctx)
	if cn, ok := w.(http.CloseNotifier); ok {
		ch := cn.CloseNotify()
		go func() {
			select {
			case <-ch:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	defer cancel()

	params, _ := ctxhelper.ParamsFromContext(ctx)

	follow := false
	if strFollow := req.FormValue("follow"); strFollow == "true" {
		follow = true
	}

	var (
		backlog bool
		lines   int
		err     error
	)
	if strLines := req.FormValue("lines"); strLines != "" {
		if lines, err = strconv.Atoi(strLines); err != nil {
			httphelper.ValidationError(w, "lines", err.Error())
			return
		}
		if lines < 0 || lines > 10000 {
			httphelper.ValidationError(w, "lines", "lines must be an integer between 0 and 10000")
			return
		}
		backlog = lines > 0
	}

	filters := make(filterSlice, 0)
	if jobID := req.FormValue("job_id"); jobID != "" {
		filters = append(filters, filterJobID(jobID))
	}
	if processTypeVals, ok := req.Form["process_type"]; ok && len(processTypeVals) > 0 {
		val := processTypeVals[len(processTypeVals)-1]
		filters = append(filters, filterProcessType(val))
	}
	if streamTypeVals := req.FormValue("stream_types"); streamTypeVals != "" {
		vals := strings.Split(streamTypeVals, ",")
		streamTypes := make([]logagg.StreamType, len(vals))
		for i, typ := range vals {
			streamTypes[i] = logagg.StreamType(typ)
		}
		filters = append(filters, filterStreamType(streamTypes...))
	}

	iter := &Iterator{
		id:      params.ByName("channel_id"),
		follow:  follow,
		backlog: backlog,
		lines:   lines,
		filter:  filters,
		donec:   ctx.Done(),
	}

	writeMessages(ctx, w, iter.Scan(a.agg))
}

func (a *aggregatorAPI) GetSnapshot(ctx context.Context, w http.ResponseWriter, req *http.Request) {
	a.snaps.wg.Add(1)
	defer a.snaps.done()
	w.Header().Set("Content-Type", "application/vnd.flynn.logaggregator-snapshot")
	// the count goes in a trailer once every message is written, so a
	// reader can tell a whole snapshot from one that ended early
	w.Header().Set("Trailer", snapshotCountTrailer)
	buffers := a.agg.ReadAll()
	if a.snaps.hook != nil {
		a.snaps.hook()
	}
	if err := snapshot.WriteTo(buffers, w); err != nil {
		log15.Error("error writing snapshot", "client_ip", req.RemoteAddr, "err", err)
		return
	}
	n := 0
	for _, buf := range buffers {
		n += len(buf)
	}
	w.Header().Set(snapshotCountTrailer, strconv.Itoa(n))
}

func writeMessages(ctx context.Context, w http.ResponseWriter, msgc <-chan *rfc5424.Message) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	enc := json.NewEncoder(w)
	for {
		select {
		case syslogMsg, ok := <-msgc:
			if !ok { // channel is closed / done
				return
			}
			if err := enc.Encode(NewMessageFromSyslog(syslogMsg)); err != nil {
				log15.Error("error writing msg", "err", err)
				return
			}
		case <-ticker.C:
			w.(http.Flusher).Flush()
		case <-ctx.Done():
			return
		}
	}
}

func NewMessageFromSyslog(m *rfc5424.Message) client.Message {
	processType, jobID := splitProcID(m.ProcID)
	stream := utils.StreamType(m)
	source := "app"
	if stream == logagg.StreamTypeSystem {
		source = "flynn"
	}
	return client.Message{
		HostID:      string(m.Hostname),
		JobID:       string(jobID),
		JobName:     structuredParam(m, "job_name"),
		Msg:         string(m.Msg),
		ProcessType: string(processType),
		Source:      source,
		Stream:      stream,
		Timestamp:   m.Timestamp,
	}
}

func structuredParam(m *rfc5424.Message, name string) string {
	if m == nil {
		return ""
	}
	sd, err := rfc5424.ParseStructuredData(m.StructuredData)
	if err != nil || sd == nil {
		return ""
	}
	want := []byte(name)
	for _, p := range sd.Params {
		if bytes.Equal(p.Name, want) {
			return string(p.Value)
		}
	}
	return ""
}

var procIDsep = []byte{'.'}

func splitProcID(procID []byte) (processType, jobID []byte) {
	split := bytes.SplitN(procID, procIDsep, 2)
	if len(split) < 2 {
		jobID = split[0]
	} else {
		processType = split[0]
		jobID = split[1]
	}
	return
}
