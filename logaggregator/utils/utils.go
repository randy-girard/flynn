package utils

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"time"

	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

func ParseMessage(data []byte) (*rfc5424.Message, *HostCursor, error) {
	msg, err := rfc5424.Parse(data)
	if err != nil {
		return nil, nil, err
	}
	c, err := ParseHostCursor(msg)
	return msg, c, err
}

func ParseHostCursor(msg *rfc5424.Message) (*HostCursor, error) {
	sd, err := rfc5424.ParseStructuredData(msg.StructuredData)
	if err != nil {
		return nil, err
	}
	if sd == nil || !bytes.Equal(sd.ID, []byte("flynn")) || len(sd.Params) == 0 {
		return nil, errors.New("missing structured data")
	}
	var c *HostCursor
	for _, p := range sd.Params {
		if !bytes.Equal(p.Name, []byte("seq")) {
			continue
		}
		seq, err := strconv.ParseUint(string(p.Value), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("error parsing seq: %s", err)
		}
		c = &HostCursor{msg.Timestamp, seq}
		break
	}
	if c == nil {
		return nil, errors.New("missing seq structured data")
	}
	return c, nil
}

type HostCursor struct {
	Time time.Time `json:"time"`
	Seq  uint64    `json:"seq"`
}

func (c HostCursor) After(other HostCursor) bool {
	return c.Time.After(other.Time) || (c.Time.Equal(other.Time) && c.Seq > other.Seq)
}

func StreamType(msg *rfc5424.Message) logagg.StreamType {
	if msg == nil {
		return logagg.StreamTypeUnknown
	}
	// Plugin sample# lines are Flynn metrics, same as host "metrics cpu_percent=…".
	// Jobs print them on stdout/stderr; treat them as system so CLI/dashboard
	// show flynn[postgres.N] in white instead of app[] in red.
	if logagg.MsgID(msg.MsgID) != logagg.MsgIDInit && IsPluginMetricsLine(msg.Msg) {
		return logagg.StreamTypeSystem
	}
	switch logagg.MsgID(msg.MsgID) {
	case logagg.MsgIDStdout:
		return logagg.StreamTypeStdout
	case logagg.MsgIDStderr:
		return logagg.StreamTypeStderr
	case logagg.MsgIDInit:
		return logagg.StreamTypeInit
	case logagg.MsgIDSystem:
		return logagg.StreamTypeSystem
	default:
		return logagg.StreamTypeUnknown
	}
}

var pluginMetricsPrefixes = [][]byte{
	[]byte("flynn-postgres "),
	[]byte("flynn-redis "),
	[]byte("heroku-postgres "),
	[]byte("heroku-redis "),
}

// IsPluginMetricsLine reports whether a job log line is a Flynn addon sample#
// (flynn-postgres / flynn-redis). Host logmux promotes these to MsgIDSystem.
func IsPluginMetricsLine(msg []byte) bool {
	line := bytes.TrimSpace(msg)
	if len(line) == 0 {
		return false
	}
	for _, p := range pluginMetricsPrefixes {
		if bytes.HasPrefix(line, p) {
			return true
		}
	}
	return false
}
