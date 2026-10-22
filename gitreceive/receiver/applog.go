package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	logagg "github.com/randy-girard/flynn/logaggregator/types"
	"github.com/randy-girard/flynn/pkg/syslog/rfc5424"
	"github.com/randy-girard/flynn/pkg/syslog/rfc6587"
)

// manifestProcessType is the log process the dashboard reads for gitreceive
// lines that are not part of the slugbuilder job: flynn-app.json, release
// creation, postdeploy, and "Application deployed".
const manifestProcessType = "flynn-app-json"

// receiveOut is the git client stream for this push, also shipped to the app
// log. It stays open until the receiver exits so postdeploy is included.
var receiveOut io.Writer = os.Stdout

func receiveWriter() io.Writer {
	if receiveOut == nil {
		return os.Stdout
	}
	return receiveOut
}

func receivePrintf(format string, args ...any) {
	fmt.Fprintf(receiveWriter(), format, args...)
}

func receivePrintln(args ...any) {
	fmt.Fprintln(receiveWriter(), args...)
}

// appProgressLog copies git-hook output and also ships each line to the
// target app's logaggregator stream.
type appProgressLog struct {
	dst   io.Writer
	buf   []byte
	conns []net.Conn
	appID string
	seq   uint64
}

func openAppProgressLog(appID string, dst io.Writer) *appProgressLog {
	w := &appProgressLog{dst: dst, appID: appID}
	if appID == "" {
		return w
	}
	addrs := logAggregatorSyslogAddrs()
	for _, addr := range addrs {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			continue
		}
		w.conns = append(w.conns, conn)
	}
	return w
}

func (w *appProgressLog) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if n > 0 {
		w.buf = append(w.buf, p[:n]...)
		for {
			i := bytes.IndexByte(w.buf, '\n')
			if i < 0 {
				break
			}
			line := string(bytes.TrimRight(w.buf[:i], "\r"))
			w.buf = append([]byte(nil), w.buf[i+1:]...)
			if line != "" {
				w.emit(line)
			}
		}
	}
	return n, err
}

func (w *appProgressLog) Close() error {
	var err error
	for _, conn := range w.conns {
		if cerr := conn.Close(); err == nil {
			err = cerr
		}
	}
	w.conns = nil
	return err
}

func (w *appProgressLog) emit(line string) {
	if w == nil || w.appID == "" || len(w.conns) == 0 {
		return
	}
	w.seq++
	frame := manifestSyslogFrame(w.appID, w.seq, line)
	for _, conn := range w.conns {
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_, _ = conn.Write(frame)
	}
}

func logAggregatorSyslogAddrs() []string {
	type result struct {
		addrs []string
	}
	ch := make(chan result, 1)
	go func() {
		insts, err := discoverd.DefaultClient.Service("logaggregator").Instances()
		if err != nil {
			ch <- result{}
			return
		}
		addrs := make([]string, 0, len(insts))
		for _, inst := range insts {
			if inst == nil || inst.Addr == "" {
				continue
			}
			addrs = append(addrs, inst.Addr)
		}
		ch <- result{addrs: addrs}
	}()
	select {
	case r := <-ch:
		return r.addrs
	case <-time.After(2 * time.Second):
		return nil
	}
}

func manifestSyslogFrame(appID string, seq uint64, line string) []byte {
	hdr := &rfc5424.Header{
		Hostname: []byte("gitreceive"),
		AppName:  []byte(appID),
		ProcID:   []byte(manifestProcessType + ".manifest"),
		MsgID:    []byte(logagg.MsgIDStdout),
		Severity: 6,
		Facility: 23,
	}
	msg := rfc5424.NewMessage(hdr, []byte(line))
	sd := &rfc5424.StructuredData{
		ID: []byte("flynn"),
		Params: []rfc5424.StructuredDataParam{{
			Name:  []byte("seq"),
			Value: []byte(strconv.FormatUint(seq, 10)),
		}},
	}
	var sdBuf bytes.Buffer
	_ = sd.Encode(&sdBuf)
	msg.StructuredData = sdBuf.Bytes()
	return rfc6587.Bytes(msg)
}
