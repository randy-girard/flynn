package main

// EDGE-37: refusing a push so that git prints why.
//
// git prints no body of a failed RPC: a push answered with 413 or 408 shows
// only "error: RPC failed; HTTP 413 curl 22 ...", whatever the body says
// (git 2.43 and 2.50, checked). What git does print is the receive-pack
// result of a 200 answer: side-band 2 lines as "remote: <line>", the unpack
// status as "error: remote unpack failed: <reason>" and each rejected ref
// as "! [remote rejected] <ref> (<reason>)". So a push gitreceive refuses
// is answered 200 with that result, when the client asked for
// report-status (every git since 1.6 does), and with the HTTP status
// otherwise.
//
// git reads the answer only after it has sent its whole request. A request
// with a Content-Length (git sends one when the pack fits http.postBuffer,
// 1 MiB by default) is already in curl's buffer, so it can be answered
// before it is read. A chunked pack is still being written by git when the
// limit is passed: answering then and closing the connection makes git
// send the rest of the pack as a new request and print "Everything
// up-to-date" after the reason. So the rest of a chunked pack is read and
// discarded first, up to MAX_PUSH_DRAIN past the limit; a pack larger than
// that gets the plain 413, which costs no more bandwidth.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"github.com/randy-girard/flynn/pkg/httphelper"
	"github.com/randy-girard/flynn/pkg/uploadlimit"
)

// defaultMaxPushDrain is how much of a chunked pack past MAX_PUSH_SIZE is
// read and discarded so the refusal reaches git, unless MAX_PUSH_DRAIN says
// otherwise. It costs bandwidth and time only: nothing is written to disk.
const defaultMaxPushDrain = 1 << 30

// maxCommandBytes bounds how much of a receive-pack request is parsed for
// its ref update commands. A push of more refs than fit is still passed to
// git; it is only refused with the plain HTTP status.
const maxCommandBytes = 1 << 20

// pushCommands is what a receive-pack request says before its pack: the
// refs it updates and the capabilities the client asked for.
type pushCommands struct {
	refs         []string
	reportStatus bool
	// sideBand is the largest packet of the side-band the client asked
	// for (1000 for side-band, 65520 for side-band-64k), 0 for none.
	sideBand int
	// complete is set when the command list ended with its flush packet.
	complete bool
	// probe is set for a request that is only a flush packet: the probe
	// git sends before a chunked pack, which updates nothing.
	probe bool
	// notGit is set for a body that does not start with a pkt-line: the
	// rest of a pack git sends as a new request after a refusal it could
	// not read to the end (see refuse), or garbage.
	notGit bool
}

// canReport reports whether a refusal can be sent as a receive-pack result
// git prints.
func (c *pushCommands) canReport() bool {
	return c != nil && c.complete && c.reportStatus && len(c.refs) > 0
}

// readPushCommands reads the command list at the start of a receive-pack
// request body. The returned reader yields the whole body again, the bytes
// already read first, for git. A body that is not a command list (the
// 4-byte probe git sends before a chunked pack, garbage) is passed on as
// it is and cannot be reported on.
func readPushCommands(r io.Reader) (*pushCommands, io.Reader) {
	var seen bytes.Buffer
	tee := io.TeeReader(r, &seen)
	c := &pushCommands{}
	capsSeen := false
	for seen.Len() < maxCommandBytes {
		var hdr [4]byte
		if _, err := io.ReadFull(tee, hdr[:]); err != nil {
			break
		}
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		if err != nil || (n > 0 && n < 4) {
			c.notGit = seen.Len() == 4
			break
		}
		if n == 0 {
			c.complete = len(c.refs) > 0
			c.probe = seen.Len() == 4
			break
		}
		line := make([]byte, n-4)
		if _, err := io.ReadFull(tee, line); err != nil {
			break
		}
		s := strings.TrimSuffix(string(line), "\n")
		if i := strings.IndexByte(s, 0); i >= 0 {
			if !capsSeen {
				capsSeen = true
				c.parseCaps(s[i+1:])
			}
			s = s[:i]
		}
		// "<old> <new> <ref>"; shallow lines and push certificate headers
		// do not match
		if f := strings.Fields(s); len(f) == 3 && isObjectID(f[0]) && isObjectID(f[1]) {
			c.refs = append(c.refs, f[2])
		}
	}
	if !c.complete {
		c.refs = nil
	}
	return c, io.MultiReader(&seen, r)
}

// isProbe reports whether c is the probe git sends before a chunked pack.
func (c *pushCommands) isProbe() bool { return c != nil && c.probe }

func (c *pushCommands) parseCaps(caps string) {
	for _, cap := range strings.Fields(caps) {
		switch cap {
		case "report-status", "report-status-v2":
			c.reportStatus = true
		case "side-band-64k":
			c.sideBand = 65520
		case "side-band":
			if c.sideBand == 0 {
				c.sideBand = 1000
			}
		}
	}
}

func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

// refusal is what git prints for a refused push: lines on side-band 2
// ("remote: <line>"), and status as the unpack error and the reason each
// ref is rejected.
type refusal struct {
	lines  []string
	status string
	// setting and value name the limit in the gitreceive log
	setting, value string
	// code is the HTTP status for a client that cannot be told otherwise
	code int
	// body is the plain answer with code (git shows it only with
	// GIT_CURL_VERBOSE or GIT_TRACE_CURL)
	body string
}

func tooLargeRefusal(limit int64) refusal {
	size := units.BytesSize(float64(limit))
	return refusal{
		lines: []string{
			fmt.Sprintf("-----> Push refused: it is larger than the gitreceive limit of %s (MAX_PUSH_SIZE).", size),
			"       Push fewer commits at a time or keep large files out of the repository, or ask an",
			"       operator to raise the limit with `flynn -a gitreceive env:set MAX_PUSH_SIZE=<size>`.",
		},
		status:  fmt.Sprintf("push larger than MAX_PUSH_SIZE (%s)", size),
		setting: "MAX_PUSH_SIZE",
		value:   uploadlimit.Format(limit),
		code:    http.StatusRequestEntityTooLarge,
		body: fmt.Sprintf("push exceeds the gitreceive limit of %s; an operator can raise it with `flynn -a gitreceive env:set MAX_PUSH_SIZE=<size>`",
			uploadlimit.Format(limit)),
	}
}

func idleRefusal(timeout time.Duration) refusal {
	return refusal{
		lines: []string{
			fmt.Sprintf("-----> Push refused: no data arrived for %s, the gitreceive upload idle timeout (PUSH_IDLE_TIMEOUT).", timeout),
			"       Check the connection and push again; an operator can change the timeout with",
			"       `flynn -a gitreceive env:set PUSH_IDLE_TIMEOUT=<duration>`.",
		},
		status:  fmt.Sprintf("no data for PUSH_IDLE_TIMEOUT (%s)", timeout),
		setting: "PUSH_IDLE_TIMEOUT",
		value:   timeout.String(),
		code:    http.StatusRequestTimeout,
		body:    fmt.Sprintf("%s (%s); an operator can change it with `flynn -a gitreceive env:set PUSH_IDLE_TIMEOUT=<duration>`", uploadlimit.ErrIdle, timeout),
	}
}

// writeRefusal writes the receive-pack result of a refused push: the lines
// on side-band 2, then a report-status that fails the unpack and rejects
// every ref, on side-band 1 when the client asked for side-band.
func writeRefusal(w io.Writer, c *pushCommands, r refusal) error {
	var status bytes.Buffer
	pktLine(&status, "unpack "+r.status+"\n")
	for _, ref := range c.refs {
		pktLine(&status, "ng "+ref+" "+r.status+"\n")
	}
	pktFlush(&status)
	if c.sideBand == 0 {
		_, err := w.Write(status.Bytes())
		return err
	}
	max := c.sideBand - 5
	var out bytes.Buffer
	band := func(ch byte, p []byte) {
		for len(p) > 0 {
			n := len(p)
			if n > max {
				n = max
			}
			fmt.Fprintf(&out, "%04x%c", n+5, ch)
			out.Write(p[:n])
			p = p[n:]
		}
	}
	for _, l := range r.lines {
		band(2, []byte(l+"\n"))
	}
	band(1, status.Bytes())
	pktFlush(&out)
	_, err := w.Write(out.Bytes())
	return err
}

// refuse answers a request gitreceive will not take, before git has written
// anything: a push whose client asked for report-status gets a 200 result
// git prints, anything else gets r.code. closeConn closes the connection
// after the answer (the body was not read to its end). received is the
// part of the body read, for the log.
func refuse(w http.ResponseWriter, env gitEnv, c *pushCommands, r refusal, closeConn bool, received int64) {
	if closeConn {
		w.Header().Set("Connection", "close")
	}
	answer := strconv.Itoa(r.code)
	if c.canReport() {
		answer = "report-status"
		w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		writeRefusal(w, c, r)
	} else {
		http.Error(w, r.body, r.code)
	}
	if _, ok := w.(*httphelper.ResponseWriter); ok {
		ctx := []interface{}{"setting", r.setting, "limit", r.value, "received_bytes", received, "answer", answer}
		if env.AppName != "" {
			ctx = append(ctx, "app", env.AppName)
		}
		if env.Pipeline != "" {
			ctx = append(ctx, "pipeline", env.Pipeline)
		}
		logError(w, "push refused", errors.New(r.status), ctx...)
	}
}

// drain reads and discards the rest of r, at most max bytes. It reports
// whether r ended within max.
func drain(r io.Reader, max int64) (int64, bool) {
	n, err := io.Copy(io.Discard, uploadlimit.NewBudgetReader(r, max))
	return n, err == nil
}
