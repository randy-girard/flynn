package httphelper

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx"
	"github.com/julienschmidt/httprouter"
	"github.com/randy-girard/flynn/pkg/cors"
	"github.com/randy-girard/flynn/pkg/ctxhelper"
	"github.com/randy-girard/flynn/pkg/dialer"
	"github.com/randy-girard/flynn/pkg/random"
	"golang.org/x/net/context"
)

type ErrorCode string

const (
	// RetryResponseHeaderTimeout bounds how long RetryClient waits for
	// response headers. SSE/log streams send headers immediately, so this
	// does not cancel an open body; a total Client.Timeout would.
	RetryResponseHeaderTimeout = 10 * time.Second
	RetryTLSHandshakeTimeout   = 10 * time.Second
	RetryIdleConnTimeout       = 90 * time.Second
)

// RetryClient is the shared HTTP client for cluster RPCs. It has no total
// Timeout so streaming (SSE/hijack) callers are not cut off after a wall
// clock; the Transport still fails hung header/dial waits.
var RetryClient = &http.Client{
	Transport: &http.Transport{
		Dial:                  dialer.Retry.Dial,
		ResponseHeaderTimeout: RetryResponseHeaderTimeout,
		TLSHandshakeTimeout:   RetryTLSHandshakeTimeout,
		IdleConnTimeout:       RetryIdleConnTimeout,
		ExpectContinueTimeout: time.Second,
	},
}

// ProvisionHeaderTimeout is how long a resource provision may run before
// response headers. RetryClient's 10s header timeout would abort the caller
// and start another database while the first one is still booting.
const ProvisionHeaderTimeout = 15 * time.Minute

// ProvisionClient is for provider provision calls only. It has no total
// Timeout, same as RetryClient, but it waits long enough for one database
// to start instead of retrying the POST.
var ProvisionClient = &http.Client{
	Transport: &http.Transport{
		Dial:                  dialer.Retry.Dial,
		ResponseHeaderTimeout: ProvisionHeaderTimeout,
		TLSHandshakeTimeout:   RetryTLSHandshakeTimeout,
		IdleConnTimeout:       RetryIdleConnTimeout,
		ExpectContinueTimeout: time.Second,
	},
}

const (
	NotFoundErrorCode           ErrorCode = "not_found"
	ObjectNotFoundErrorCode     ErrorCode = "object_not_found"
	ObjectExistsErrorCode       ErrorCode = "object_exists"
	ConflictErrorCode           ErrorCode = "conflict"
	SyntaxErrorCode             ErrorCode = "syntax_error"
	ValidationErrorCode         ErrorCode = "validation_error"
	PreconditionFailedErrorCode ErrorCode = "precondition_failed"
	UnauthorizedErrorCode       ErrorCode = "unauthorized"
	ForbiddenErrorCode          ErrorCode = "forbidden"
	UnknownErrorCode            ErrorCode = "unknown_error"
	RatelimitedErrorCode        ErrorCode = "ratelimited"
	ServiceUnavailableErrorCode ErrorCode = "service_unavailable"
	RequestBodyTooBigErrorCode  ErrorCode = "request_body_too_big"
)

var ErrRequestBodyTooBig = errors.New("httphelper: request body too big")

var errorResponseCodes = map[ErrorCode]int{
	NotFoundErrorCode:           404,
	ObjectNotFoundErrorCode:     404,
	ObjectExistsErrorCode:       409,
	ConflictErrorCode:           409,
	PreconditionFailedErrorCode: 412,
	SyntaxErrorCode:             400,
	ValidationErrorCode:         400,
	RequestBodyTooBigErrorCode:  400,
	UnauthorizedErrorCode:       401,
	ForbiddenErrorCode:          403,
	UnknownErrorCode:            500,
	RatelimitedErrorCode:        429,
	ServiceUnavailableErrorCode: 503,
}

type JSONError struct {
	Code    ErrorCode       `json:"code"`
	Message string          `json:"message"`
	Detail  json.RawMessage `json:"detail,omitempty"`
	Retry   bool            `json:"retry"`
}

func isJSONErrorWithCode(err error, code ErrorCode) bool {
	e, ok := err.(JSONError)
	return ok && e.Code == code
}

func IsObjectNotFoundError(err error) bool {
	return isJSONErrorWithCode(err, ObjectNotFoundErrorCode)
}

func IsObjectExistsError(err error) bool {
	return isJSONErrorWithCode(err, ObjectExistsErrorCode)
}

func IsPreconditionFailedError(err error) bool {
	return isJSONErrorWithCode(err, PreconditionFailedErrorCode)
}

func IsValidationError(err error) bool {
	return isJSONErrorWithCode(err, ValidationErrorCode)
}

// IsForbidden reports whether err is a JSONError carrying ForbiddenErrorCode (HTTP 403).
func IsForbidden(err error) bool {
	return isJSONErrorWithCode(err, ForbiddenErrorCode)
}

// IsRetryableError indicates whether a HTTP request can be safely retried.
func IsRetryableError(err error) bool {
	var e JSONError
	if errors.As(err, &e) {
		return e.Retry
	}
	return false
}

// SEC-016: CORSAllowAll defaults to permissive CORS for backwards compatibility.
// Set CORS_ALLOWED_ORIGINS environment variable to a comma-separated list of
// allowed origins to restrict cross-origin requests.
var CORSAllowAll = newCORSOptions()

func newCORSOptions() *cors.Options {
	opts := &cors.Options{
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"},
		AllowHeaders:     []string{"Auth-Key", "Authorization", "Accept", "Content-Type", "If-Match", "If-None-Match", "X-GRPC-Web"},
		ExposeHeaders:    []string{"ETag", "Content-Disposition"},
		AllowCredentials: true,
		MaxAge:           time.Hour,
	}
	if origins := os.Getenv("CORS_ALLOWED_ORIGINS"); origins != "" {
		opts.AllowOrigins = strings.Split(origins, ",")
	} else {
		opts.AllowAllOrigins = true
	}
	return opts
}

// Handler is an extended version of http.Handler that also takes a context
// argument ctx.
type Handler interface {
	ServeHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request)
}

// The HandlerFunc type is an adapter to allow the use of ordinary functions as
// Handlers.  If f is a function with the appropriate signature, HandlerFunc(f)
// is a Handler object that calls f.
type HandlerFunc func(context.Context, http.ResponseWriter, *http.Request)

// ServeHTTP calls f(ctx, w, r).
func (f HandlerFunc) ServeHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	f(ctx, w, r)
}

func WrapHandler(handler HandlerFunc) httprouter.Handle {
	return func(w http.ResponseWriter, req *http.Request, params httprouter.Params) {
		ctx := contextFromResponseWriter(w)
		ctx = ctxhelper.NewContextParams(ctx, params)
		handler.ServeHTTP(ctx, w, req)
	}
}

func ContextInjector(componentName string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		reqID := req.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = random.UUID()
		}
		ctx := ctxhelper.NewContextRequestID(context.Background(), reqID)
		ctx = ctxhelper.NewContextComponentName(ctx, componentName)
		rw := NewResponseWriter(w, ctx)
		handler.ServeHTTP(rw, req)
	})
}

func contextFromResponseWriter(w http.ResponseWriter) context.Context {
	ctx := w.(*ResponseWriter).Context()
	return ctx
}

func (jsonError JSONError) Error() string {
	return fmt.Sprintf("%s: %s", jsonError.Code, jsonError.Message)
}

func logError(w http.ResponseWriter, err error) {
	if rw, ok := w.(*ResponseWriter); ok {
		logger, _ := ctxhelper.LoggerFromContext(rw.Context())
		logger.Error(err.Error())
	} else {
		log.Println(err)
	}
}

// buildJSONError returns an appropriate API error to send to clients based
// on the given internal error.
//
// We consider all postgres errors as retry-able as they usually occur when
// postgres is read-only (for example during a system update). Data related
// postgres errors should in general not be retried (because a retry will
// likely result in the same error), but it is expected that such errors are
// caught and a validation error returned to the client rather than the
// postgres error.
//
// Errors returned from "net" are also considered retryable because they
// generally occur when the process is trying to reach another resource which
// may be down temporarily.
// This also includes syscall.Errno, which is notable because it can also be
// returned from file operations among other things.
// It's expected if you don't want clients to retry on these errors they
// should be caught and a more appropriate error returned to the caller.
func buildJSONError(err error) *JSONError {
	jsonError := &JSONError{
		Code:    UnknownErrorCode,
		Message: "Something went wrong",
	}
	if err == ErrRequestBodyTooBig {
		return &JSONError{
			Code:    RequestBodyTooBigErrorCode,
			Message: "The provided request body is too big",
		}
	}
	switch v := err.(type) {
	case *json.SyntaxError, *json.UnmarshalTypeError:
		jsonError = &JSONError{
			Code:    SyntaxErrorCode,
			Message: "The provided JSON input is invalid",
		}
	case pgx.PgError:
		jsonError.Retry = true
		jsonError.Message = v.Error()
	case *net.OpError:
		jsonError.Retry = true
		jsonError.Message = v.Error()
	case syscall.Errno:
		jsonError.Retry = true
		jsonError.Message = v.Error()
	case JSONError:
		jsonError = &v
	case *JSONError:
		jsonError = v
	default:
		if err == pgx.ErrDeadConn {
			jsonError.Retry = true
			jsonError.Message = err.Error()
		} else {
			var pgErr pgx.PgError
			if errors.As(err, &pgErr) {
				jsonError.Retry = true
				jsonError.Message = pgErr.Error()
			}
		}
	}
	return jsonError
}

func Error(w http.ResponseWriter, err error) {
	if rw, ok := w.(*ResponseWriter); !ok || (ok && rw.Status() == 0) {
		jsonError := buildJSONError(err)
		if jsonError.Code == UnknownErrorCode {
			logError(w, err)
		}
		responseCode, ok := errorResponseCodes[jsonError.Code]
		if !ok {
			responseCode = 500
		}
		JSON(w, responseCode, jsonError)
	} else {
		logError(w, err)
	}
}

func ObjectNotFoundError(w http.ResponseWriter, message string) {
	Error(w, JSONError{Code: ObjectNotFoundErrorCode, Message: message})
}

func ObjectExistsErr(message string) error {
	return JSONError{Code: ObjectExistsErrorCode, Message: message}
}

func ObjectExistsError(w http.ResponseWriter, message string) {
	Error(w, ObjectExistsErr(message))
}

func ConflictError(w http.ResponseWriter, message string) {
	Error(w, JSONError{Code: ConflictErrorCode, Message: message})
}

func PreconditionFailedErr(message string) error {
	return JSONError{Code: PreconditionFailedErrorCode, Message: message}
}

func ServiceUnavailableError(w http.ResponseWriter, message string) {
	Error(w, JSONError{Code: ServiceUnavailableErrorCode, Message: message, Retry: true})
}

func ValidationError(w http.ResponseWriter, field, message string) {
	err := JSONError{Code: ValidationErrorCode, Message: message}
	if field != "" {
		err.Message = fmt.Sprintf("%s %s", field, message)
		err.Detail, _ = json.Marshal(map[string]string{"field": field})
	}
	Error(w, err)
}

// Forbidden responds with HTTP 403 and a JSON error body.
func Forbidden(w http.ResponseWriter, message string) {
	JSON(w, errorResponseCodes[ForbiddenErrorCode], JSONError{Code: ForbiddenErrorCode, Message: message})
}

func JSON(w http.ResponseWriter, status int, v interface{}) {
	// Encode nil slices as `[]` instead of `null`
	if rv := reflect.ValueOf(v); rv.Type().Kind() == reflect.Slice && rv.IsNil() {
		v = []struct{}{}
	}

	var result []byte
	var err error
	result, err = json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(result)
}

func DecodeJSON(req *http.Request, i interface{}) error {
	if !strings.Contains(req.Header.Get("Content-Type"), "application/json") {
		return JSONError{Code: ValidationErrorCode, Message: "Content-Type must be application/json"}
	}
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	return dec.Decode(i)
}
