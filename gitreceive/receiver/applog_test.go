package main

import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/randy-girard/flynn/logaggregator/utils"
	"github.com/randy-girard/flynn/pkg/joboutput"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
)

func TestAppProgressLogKeepsLines(t *testing.T) {
	w := &appProgressLog{dst: io.Discard}
	if _, err := io.WriteString(w, "-----> Creating release\n=====> Application deployed\n"); err != nil {
		t.Fatal(err)
	}
	got := w.Text()
	want := "-----> Creating release\n=====> Application deployed\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAppProgressLogCapsTheTail(t *testing.T) {
	w := &appProgressLog{dst: io.Discard}
	head := strings.Repeat("a", joboutput.MaxBytes)
	if _, err := io.WriteString(w, head+"\nTAIL\n"); err != nil {
		t.Fatal(err)
	}
	got := w.Text()
	if !strings.HasSuffix(got, "TAIL\n") {
		t.Fatalf("tail missing: %q", got[len(got)-20:])
	}
	if len(got) > joboutput.MaxBytes {
		t.Fatalf("len %d exceeds %d", len(got), joboutput.MaxBytes)
	}
}

func TestAppProgressLogShipsEachLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 8192)
		n, _ := conn.Read(buf)
		got <- append([]byte(nil), buf[:n]...)
	}()

	var git bytes.Buffer
	w := &appProgressLog{dst: &git, appID: "app-1"}
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w.conns = []net.Conn{conn}
	defer w.Close()

	if _, err := io.WriteString(w, "-----> Applying flynn-app.json\n-----> Provisioning addon postgres\n"); err != nil {
		t.Fatal(err)
	}
	if git.String() != "-----> Applying flynn-app.json\n-----> Provisioning addon postgres\n" {
		t.Fatalf("git output %q", git.String())
	}
	if w.Text() != "-----> Applying flynn-app.json\n-----> Provisioning addon postgres\n" {
		t.Fatalf("kept %q", w.Text())
	}
	var frame []byte
	select {
	case frame = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for syslog")
	}
	msg, cur, err := readOneFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if cur == nil || cur.Seq != 1 {
		t.Fatalf("cursor %+v", cur)
	}
	if string(msg.AppName) != "app-1" || string(msg.ProcID) != manifestProcessType+".manifest" {
		t.Fatalf("app %q proc %q", msg.AppName, msg.ProcID)
	}
	if string(msg.Msg) != "-----> Applying flynn-app.json" {
		t.Fatalf("msg %q", msg.Msg)
	}
}

func TestManifestSyslogFrameParses(t *testing.T) {
	msg, cur, err := readOneFrame(manifestSyslogFrame("app-1", 4, "-----> Provisioning addon kafka"))
	if err != nil {
		t.Fatal(err)
	}
	if cur.Seq != 4 || string(msg.Msg) != "-----> Provisioning addon kafka" {
		t.Fatalf("msg %q seq %d", msg.Msg, cur.Seq)
	}
}

func readOneFrame(frame []byte) (*rfc5424.Message, *utils.HostCursor, error) {
	i := bytes.IndexByte(frame, ' ')
	if i < 0 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	n, err := strconv.Atoi(string(frame[:i]))
	if err != nil {
		return nil, nil, err
	}
	if i+1+n > len(frame) {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return utils.ParseMessage(frame[i+1 : i+1+n])
}
