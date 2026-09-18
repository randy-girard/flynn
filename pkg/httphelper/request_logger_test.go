package httphelper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	log "github.com/inconshreveable/log15"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"golang.org/x/net/context"
)

func TestRequestLoggerUsesLastForwardedForHop(t *testing.T) {
	var gotIP string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := NewRequestLoggerCustom(inner, func(handler http.Handler, logger log.Logger, clientIP string, rw *ResponseWriter, req *http.Request) {
		gotIP = clientIP
		handler.ServeHTTP(rw, req)
	})

	ctx := ctxhelper.NewContextComponentName(context.Background(), "test")
	ctx = ctxhelper.NewContextRequestID(ctx, "req-1")
	rec := httptest.NewRecorder()
	rw := NewResponseWriter(rec, ctx)
	req := httptest.NewRequest("GET", "/secret", nil)
	req.Header.Set("X-Forwarded-For", "1.1.1.1, 10.0.0.2")
	req.RemoteAddr = "192.0.2.1:1234"
	h.ServeHTTP(rw, req)
	if gotIP != "10.0.0.2" {
		t.Fatalf("client IP=%q, want last X-Forwarded-For hop", gotIP)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestRequestLoggerFallsBackToRemoteAddr(t *testing.T) {
	var gotIP string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := NewRequestLoggerCustom(inner, func(handler http.Handler, logger log.Logger, clientIP string, rw *ResponseWriter, req *http.Request) {
		gotIP = clientIP
		handler.ServeHTTP(rw, req)
	})
	rec := httptest.NewRecorder()
	rw := NewResponseWriter(rec, context.Background())
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.9:9"
	h.ServeHTTP(rw, req)
	if gotIP != "192.0.2.9" {
		t.Fatalf("client IP=%q", gotIP)
	}
}

func TestNewRequestLoggerCompletes(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := NewRequestLogger(inner)
	rec := httptest.NewRecorder()
	rw := NewResponseWriter(rec, context.Background())
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rw, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestResponseWriterStatusFlushAndHijack(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewResponseWriter(rec, context.Background())
	if rw.Written() || rw.Status() != 0 {
		t.Fatal("unwritten")
	}
	rw.Header().Set("X-Test", "1")
	if _, err := rw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	rw.WriteHeader(http.StatusCreated)
	if rw.Status() != http.StatusCreated {
		t.Fatalf("status=%d", rw.Status())
	}
	rw.Flush()
	if _, _, err := rw.Hijack(); err == nil {
		t.Fatal("httptest recorder is not a hijacker")
	}
}
