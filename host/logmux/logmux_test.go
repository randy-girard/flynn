package logmux

import (
	"io"
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

func TestMuxWriteSystemLine(t *testing.T) {
	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 1)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	m.Write(logagg.MsgIDSystem, &Config{
		AppID:   "app-1",
		HostID:  "host1",
		JobType: "web",
		JobID:   "host1-abc",
		JobName: "web.1",
	}, "Starting web process (runtime profile large)")
	select {
	case msg := <-ch:
		if string(msg.Msg) != "Starting web process (runtime profile large)" {
			t.Fatalf("msg=%q", msg.Msg)
		}
		if string(msg.MsgID) != string(logagg.MsgIDSystem) {
			t.Fatalf("msgid=%s", msg.MsgID)
		}
		if string(msg.ProcID) != "web.host1-abc" {
			t.Fatalf("procid=%s (must keep host job id for filters)", msg.ProcID)
		}
		if utils.StreamType(msg.Message) != logagg.StreamTypeSystem {
			t.Fatalf("stream=%s", utils.StreamType(msg.Message))
		}
		sd, err := rfc5424.ParseStructuredData(msg.StructuredData)
		if err != nil {
			t.Fatal(err)
		}
		if got := sdParam(sd, "job_name"); got != "web.1" {
			t.Fatalf("job_name=%q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log line")
	}
}

func TestMuxFollowKeepsJobIDInProcID(t *testing.T) {
	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 1)
	unsub := m.subscribe("app-1", ch)
	defer unsub()
	r, w := io.Pipe()
	stream := m.Follow(r, "", logagg.MsgIDStdout, &Config{
		AppID:   "app-1",
		HostID:  "host1",
		JobType: "web",
		JobID:   "localhost-a0ee10eb-a24b-4bab-8f58-87cada82a68c",
		JobName: "web.1",
	})
	if _, err := w.Write([]byte("hello from web\n")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	select {
	case msg := <-ch:
		if string(msg.ProcID) != "web.localhost-a0ee10eb-a24b-4bab-8f58-87cada82a68c" {
			t.Fatalf("procid=%s", msg.ProcID)
		}
		sd, err := rfc5424.ParseStructuredData(msg.StructuredData)
		if err != nil {
			t.Fatal(err)
		}
		if got := sdParam(sd, "job_name"); got != "web.1" {
			t.Fatalf("job_name=%q", got)
		}
		if string(msg.Msg) != "hello from web" {
			t.Fatalf("msg=%q", msg.Msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log line")
	}
	stream.Close()
}

func sdParam(sd *rfc5424.StructuredData, name string) string {
	if sd == nil {
		return ""
	}
	for _, p := range sd.Params {
		if string(p.Name) == name {
			return string(p.Value)
		}
	}
	return ""
}

func TestMuxWriteSkipsEmptyApp(t *testing.T) {
	m := New("host1", t.TempDir(), log15.New())
	ch := make(chan message, 1)
	unsub := m.subscribe("", ch)
	defer unsub()
	m.Write(logagg.MsgIDSystem, &Config{HostID: "host1", JobID: "j"}, "nope")
	select {
	case <-ch:
		t.Fatal("must not write without an app id")
	case <-time.After(50 * time.Millisecond):
	}
}
