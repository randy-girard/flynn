package logaggregator

import (
	"net/url"
	"strconv"
	"strings"
)

type LogOpts struct {
	Follow      bool
	JobID       string
	Lines       *int
	ProcessType *string
	StreamTypes []StreamType
}

func (o *LogOpts) EncodedQuery() string {
	query := url.Values{}
	if o.Follow {
		query.Set("follow", "true")
	}
	if o.JobID != "" {
		query.Set("job_id", o.JobID)
	}
	if o.Lines != nil && *o.Lines >= 0 {
		query.Set("lines", strconv.Itoa(*o.Lines))
	}
	if o.ProcessType != nil {
		query.Set("process_type", *o.ProcessType)
	}
	if len(o.StreamTypes) > 0 {
		streamTypes := make([]string, len(o.StreamTypes))
		for i, typ := range o.StreamTypes {
			streamTypes[i] = string(typ)
		}
		query.Set("stream_types", strings.Join(streamTypes, ","))
	} else {
		query.Set("stream_types", DefaultStreamTypesQuery())
	}
	return query.Encode()
}

type StreamType string

const (
	StreamTypeStdout  StreamType = "stdout"
	StreamTypeStderr  StreamType = "stderr"
	StreamTypeInit    StreamType = "init"
	StreamTypeSystem  StreamType = "system"
	StreamTypeUnknown StreamType = "unknown"
)

// DefaultStreamTypes is what `flynn log` and the dashboard show without --init:
// process stdout/stderr plus Flynn lifecycle lines (start, restart, scale, …).
func DefaultStreamTypes() []StreamType {
	return []StreamType{StreamTypeStdout, StreamTypeStderr, StreamTypeSystem}
}

func DefaultStreamTypesQuery() string {
	parts := DefaultStreamTypes()
	s := make([]string, len(parts))
	for i, t := range parts {
		s[i] = string(t)
	}
	return strings.Join(s, ",")
}

type MsgID string

const (
	MsgIDStdout MsgID = "ID1"
	MsgIDStderr MsgID = "ID2"
	MsgIDInit   MsgID = "ID3"
	MsgIDSystem MsgID = "ID4"
)
