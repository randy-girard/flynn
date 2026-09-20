package httphelper

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/jackc/pgx"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
)

func decodeJSONError(t *testing.T, rec *httptest.ResponseRecorder) JSONError {
	t.Helper()
	var je JSONError
	if err := json.Unmarshal(rec.Body.Bytes(), &je); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return je
}

func TestErrorStatusAndRetryMapping(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   ErrorCode
		retry  bool
	}{
		{JSONError{Code: UnauthorizedErrorCode, Message: "nope"}, 401, UnauthorizedErrorCode, false},
		{JSONError{Code: ObjectNotFoundErrorCode, Message: "gone"}, 404, ObjectNotFoundErrorCode, false},
		{ErrRequestBodyTooBig, 400, RequestBodyTooBigErrorCode, false},
		{&json.SyntaxError{}, 400, SyntaxErrorCode, false},
		{pgx.PgError{Message: "read-only"}, 500, UnknownErrorCode, true},
		{&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, 500, UnknownErrorCode, true},
		{pgx.ErrDeadConn, 500, UnknownErrorCode, true},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		Error(rec, tc.err)
		if rec.Code != tc.status {
			t.Errorf("%v: status %d want %d body=%s", tc.err, rec.Code, tc.status, rec.Body.String())
			continue
		}
		je := decodeJSONError(t, rec)
		if je.Code != tc.code || je.Retry != tc.retry {
			t.Errorf("%v: %+v", tc.err, je)
		}
	}
}

func TestJSONErrorHelpersAndJSONNilSlice(t *testing.T) {
	if !IsObjectNotFoundError(JSONError{Code: ObjectNotFoundErrorCode}) {
		t.Fatal("object not found")
	}
	if !IsObjectExistsError(ObjectExistsErr("dup")) {
		t.Fatal("exists")
	}
	if !IsPreconditionFailedError(PreconditionFailedErr("etag")) {
		t.Fatal("precondition")
	}
	if !IsValidationError(JSONError{Code: ValidationErrorCode, Message: "bad"}) {
		t.Fatal("validation")
	}
	if !IsForbidden(JSONError{Code: ForbiddenErrorCode, Message: "no"}) {
		t.Fatal("forbidden")
	}
	if IsRetryableError(JSONError{Code: UnknownErrorCode}) {
		t.Fatal("non-retry")
	}
	if !IsRetryableError(JSONError{Code: UnknownErrorCode, Retry: true}) {
		t.Fatal("retry")
	}

	rec := httptest.NewRecorder()
	var nilSlice []string
	JSON(rec, 200, nilSlice)
	if rec.Body.String() != "[]" {
		t.Fatalf("nil slice: %q", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	Forbidden(rec, "denied")
	if rec.Code != 403 {
		t.Fatalf("forbidden status %d", rec.Code)
	}
	if !IsForbidden(decodeJSONError(t, rec)) {
		t.Fatal("forbidden body")
	}

	rec = httptest.NewRecorder()
	ValidationError(rec, "name", "is required")
	je := decodeJSONError(t, rec)
	if je.Message != "name is required" || rec.Code != 400 {
		t.Fatalf("%d %+v", rec.Code, je)
	}
}

func TestDecodeJSONRequiresContentType(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	var out map[string]int
	err := DecodeJSON(req, &out)
	if !IsValidationError(err) {
		t.Fatalf("want validation, got %v", err)
	}
	req = httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	if err := DecodeJSON(req, &out); err != nil || out["a"] != 1 {
		t.Fatalf("%v %v", out, err)
	}
}

func TestCORSOriginsFromEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	open := newCORSOptions()
	if !open.AllowAllOrigins {
		t.Fatal("default must allow all origins")
	}
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example,https://b.example")
	restricted := newCORSOptions()
	if restricted.AllowAllOrigins {
		t.Fatal("explicit origins must not allow all")
	}
	if len(restricted.AllowOrigins) != 2 || restricted.AllowOrigins[0] != "https://a.example" {
		t.Fatalf("origins=%v", restricted.AllowOrigins)
	}
}

func TestContextInjectorPreservesOrAllocatesRequestID(t *testing.T) {
	h := ContextInjector("controller", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := w.(*ResponseWriter)
		id, _ := ctxhelper.RequestIDFromContext(rw.Context())
		name, _ := ctxhelper.ComponentNameFromContext(rw.Context())
		io.WriteString(w, id+"|"+name)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "req-fixed")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "req-fixed|controller" {
		t.Fatalf("got %q", rec.Body.String())
	}

	req = httptest.NewRequest("GET", "/", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "|controller") || strings.HasPrefix(rec.Body.String(), "|") {
		t.Fatalf("generated id: %q", rec.Body.String())
	}
}

func TestFlushWriter(t *testing.T) {
	var buf bytes.Buffer
	n, err := (FlushWriter{Writer: &buf, Enabled: true}).Write([]byte("ok"))
	if err != nil || n != 2 || buf.String() != "ok" {
		t.Fatalf("%d %v %q", n, err, buf.String())
	}
}

func TestRetryClientHasNoTotalTimeout(t *testing.T) {
	if RetryClient == nil || RetryClient.Transport == nil {
		t.Fatal("RetryClient must be configured")
	}
	if RetryClient.Timeout != 0 {
		t.Fatalf("RetryClient.Timeout=%s; total Timeout would cancel SSE streams", RetryClient.Timeout)
	}
	tr, ok := RetryClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("RetryClient.Transport is %T", RetryClient.Transport)
	}
	if tr.ResponseHeaderTimeout != RetryResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout=%s want %s", tr.ResponseHeaderTimeout, RetryResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != RetryTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout=%s want %s", tr.TLSHandshakeTimeout, RetryTLSHandshakeTimeout)
	}
	if tr.Dial == nil {
		t.Fatal("Dial must be set (retry dialer)")
	}
}
