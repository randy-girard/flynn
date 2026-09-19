package logmux

import (
	"testing"
	"time"

	"github.com/inconshreveable/log15"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/logaggregator/utils"
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
			t.Fatalf("procid=%s", msg.ProcID)
		}
		if utils.StreamType(msg.Message) != logagg.StreamTypeSystem {
			t.Fatalf("stream=%s", utils.StreamType(msg.Message))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for log line")
	}
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
