package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	log "github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/controller/redact"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

const maxAuditRequestBodySize = 1000000

func handleRequestWithAuditBodyBuffer(h http.Handler, rw *httphelper.ResponseWriter, req *http.Request) *bytes.Buffer {
	var buf *bytes.Buffer
	if req.Body != nil && strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		buf = &bytes.Buffer{}
		req.Body = struct {
			io.Reader
			io.Closer
		}{
			io.TeeReader(&limitedBodyReader{R: req.Body, N: maxAuditRequestBodySize}, buf),
			req.Body,
		}
	}
	h.ServeHTTP(rw, req)
	maybeRedactBody(req, buf)
	return buf
}

type limitedBodyReader struct {
	R io.Reader // underlying reader
	N int64     // max bytes remaining
}

const redactedPlaceholder = redact.Placeholder

// auditBodyLoggable is the allowlist of requests whose JSON bodies
// AUDIT_LOG=true may log (after redact.JSON). These bodies describe app
// configuration; secret-named fields and env credentials in them are still
// replaced. Every other body (users, tokens, providers, resources, sinks,
// GitHub, ACME, certificates, domain, regions, backups, plugin add-ons) is
// logged as its field names only, so a new route cannot leak by default.
func auditBodyLoggable(method, path string) bool {
	p := strings.Split(strings.Trim(path, "/"), "/")
	n := len(p)
	switch {
	case p[0] == "apps" && n == 1 && method == http.MethodPost:
		return true
	case p[0] == "apps" && n == 2 && method == http.MethodPost:
		return true
	case p[0] == "apps" && n >= 3:
		switch sub := p[2]; {
		case sub == "meta" && method == http.MethodPost,
			sub == "formations" && n == 4 && method == http.MethodPut,
			sub == "scale" && n == 4 && method == http.MethodPut,
			sub == "jobs" && n == 3 && method == http.MethodPost,
			sub == "deploy" && n == 3 && method == http.MethodPost,
			sub == "release" && n == 3 && method == http.MethodPut,
			sub == "routes" && n == 3 && method == http.MethodPost,
			sub == "routes" && n == 5 && method == http.MethodPut,
			sub == "transfer" && n == 3 && method == http.MethodPost,
			sub == "collaborators" && n == 3 && method == http.MethodPost:
			return true
		}
	case p[0] == "releases" && n == 1 && method == http.MethodPost:
		return true
	case p[0] == "accounts" && n == 3 && (p[2] == "quota" && method == http.MethodPut || p[2] == "collaborators" && method == http.MethodPost):
		return true
	case p[0] == "cluster" && n == 2 && method == http.MethodPut:
		switch p[1] {
		case "policy", "runtime-settings", "db-runtime-settings", "app-domain", "region", "placement":
			return true
		}
		return strings.HasSuffix(p[1], "-availability")
	case p[0] == "runtimes" || p[0] == "db-runtimes":
		return method == http.MethodPost || method == http.MethodPut
	case p[0] == "hosts" && n == 2 && method == http.MethodPut:
		return true
	}
	return false
}

// maybeRedactBody rewrites buf, the captured request body, into what the
// audit log may print: an allowlisted body with its secrets replaced, or
// only the field names of any other body.
func maybeRedactBody(req *http.Request, buf *bytes.Buffer) {
	if buf == nil || buf.Len() == 0 {
		return
	}
	raw := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	if auditBodyLoggable(req.Method, req.URL.Path) {
		if out, err := redact.JSON(raw); err == nil {
			buf.Write(out)
			return
		}
		buf.WriteString("[body not logged: not JSON]")
		return
	}
	buf.WriteString("[body not logged; fields: " + strings.Join(redact.Fields(raw), ",") + "]")
}

func (l *limitedBodyReader) Read(p []byte) (n int, err error) {
	if l.N <= 0 {
		return 0, httphelper.ErrRequestBodyTooBig
	}
	if int64(len(p)) > l.N {
		p = p[0:l.N]
	}
	n, err = l.R.Read(p)
	l.N -= int64(n)
	return
}

func auditLoggerFn(handler http.Handler, logger log.Logger, clientIP string, rw *httphelper.ResponseWriter, req *http.Request) {
	start := time.Now()
	logger.Info("request started", "method", req.Method, "path", req.URL.Path, "client_ip", clientIP)
	bodyBuf := handleRequestWithAuditBodyBuffer(handler, rw, req)
	logger.Info("request completed", "status", rw.Status(), "duration", time.Since(start), "method", req.Method, "path", req.URL.Path, "client_ip", clientIP, "auth_id", req.Header.Get("Flynn-Auth-ID"), "auth_user", req.Header.Get("Flynn-Auth-User"), "user_agent", req.Header.Get("User-Agent"), "body", bodyBuf.String())
}
