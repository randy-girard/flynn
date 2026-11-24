package deployment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inconshreveable/log15"
	controller "github.com/randy-girard/flynn/controller/client"
	ct "github.com/randy-girard/flynn/controller/types"
	worker "github.com/randy-girard/flynn/controller/worker/types"
	"github.com/randy-girard/flynn/pkg/httpclient"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

// A deploy makes many controller calls while it rolls an app, and the
// controller answering them can be the one being replaced. One timed-out
// JobList (deployment c7b6063a: `timeout awaiting response headers` right
// after the last old scheduler was scaled to zero) used to fail the whole
// controller deploy and roll it back (EDGE-65). Now a wait loop counts a
// failed poll as a missed poll until its own deadline, and a one-off call is
// retried with backoff for apiRetryBudget. Each failure is logged with its
// attempt count. Errors that are answers rather than outages (a 404 for a
// deleted app or release, 401, 403, a validation error, a conflict) still
// fail at once, as does a stopped worker.
//
// These are vars so tests can shorten them.
var (
	apiRetryBudget     = 30 * time.Second
	apiRetryBackoff    = 250 * time.Millisecond
	apiRetryMaxBackoff = 2 * time.Second
)

// permanentAPIError reports whether err is an answer that a retry would not
// change. Everything else (timeouts, refused or reset connections, EOF, 5xx,
// discoverd's 503 while it catches up, a lookup that found no controller) is
// treated as transient.
func permanentAPIError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, worker.ErrStopped) || errors.Is(err, ct.ErrScalingStopped) {
		return true
	}
	if errors.Is(err, controller.ErrNotFound) || httpclient.IsUnauthorized(err) {
		return true
	}
	var je httphelper.JSONError
	if errors.As(err, &je) {
		switch je.Code {
		case httphelper.NotFoundErrorCode, httphelper.ObjectNotFoundErrorCode,
			httphelper.ObjectExistsErrorCode, httphelper.ConflictErrorCode,
			httphelper.PreconditionFailedErrorCode, httphelper.SyntaxErrorCode,
			httphelper.ValidationErrorCode, httphelper.RequestBodyTooBigErrorCode,
			httphelper.UnauthorizedErrorCode, httphelper.ForbiddenErrorCode:
			return true
		}
		return false
	}
	// A non-JSON error status (httpclient: raw req: unexpected status N).
	msg := err.Error()
	for _, code := range []string{"400", "401", "403", "404", "409", "412", "413"} {
		if strings.Contains(msg, "unexpected status "+code) {
			return true
		}
	}
	return false
}

func nextAPIBackoff(b time.Duration) time.Duration {
	if b <= 0 {
		return apiRetryBackoff
	}
	if b *= 2; b > apiRetryMaxBackoff {
		b = apiRetryMaxBackoff
	}
	return b
}

// retryAPI runs one controller call of the deploy, retrying a transient
// error with backoff for apiRetryBudget. A permanent error is returned as
// is, so callers can still compare it with controller.ErrNotFound. When the
// budget runs out the error names the call, the attempts, the elapsed time
// and the last error.
func (d *DeployJob) retryAPI(what string, log log15.Logger, fn func() error) error {
	if log == nil {
		log = d.logger
	}
	return retryAPICall(what, apiRetryBudget, d.stop, log, fn)
}

func retryAPICall(what string, budget time.Duration, stop <-chan struct{}, log log15.Logger, fn func() error) error {
	start := time.Now()
	deadline := start.Add(budget)
	backoff := apiRetryBackoff
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil {
			if attempt > 1 && log != nil {
				log.Info("controller call answered after retrying", "call", what, "attempts", attempt, "waited", time.Since(start).Round(time.Millisecond))
			}
			return nil
		}
		if permanentAPIError(err) {
			return err
		}
		waited := time.Since(start).Round(time.Millisecond)
		if !time.Now().Add(backoff).Before(deadline) {
			if log != nil {
				log.Error("controller call kept failing, giving up", "call", what, "attempts", attempt, "waited", waited, "budget", budget, "err", err)
			}
			return fmt.Errorf("%s: no answer from the controller within %s (%d attempts, %s elapsed); last error: %w", what, budget, attempt, waited, err)
		}
		if log != nil {
			log.Warn("controller call failed, retrying", "call", what, "attempt", attempt, "waited", waited, "budget", budget, "err", err)
		}
		select {
		case <-stop:
			return worker.ErrStopped
		case <-time.After(backoff):
		}
		backoff = nextAPIBackoff(backoff)
	}
}

// missedPolls tracks the failed polls of one wait loop. A transient error is
// a missed poll: the loop logs it, backs off and polls again until its own
// deadline. A permanent error ends the loop at once.
type missedPolls struct {
	loop    string
	call    string
	started time.Time
	failed  int
	streak  int
	last    error
	backoff time.Duration
}

func newMissedPolls(loop, call string) *missedPolls {
	return &missedPolls{loop: loop, call: call, started: time.Now()}
}

// miss records a failed poll. It returns err when the loop must stop.
func (m *missedPolls) miss(err error, deadline time.Time, log log15.Logger) error {
	if permanentAPIError(err) {
		return err
	}
	m.failed++
	m.streak++
	m.last = err
	m.backoff = nextAPIBackoff(m.backoff)
	if log != nil {
		log.Warn("missed poll, retrying until the loop's deadline", "loop", m.loop, "call", m.call, "attempt", m.streak,
			"failed", m.failed, "waited", m.elapsed(), "remaining", time.Until(deadline).Round(time.Millisecond), "err", err)
	}
	return nil
}

// answered records a poll that got an answer.
func (m *missedPolls) answered(log log15.Logger) {
	if m.streak > 0 && log != nil {
		log.Info("poll answered again", "loop", m.loop, "call", m.call, "missed", m.streak, "waited", m.elapsed())
	}
	m.streak = 0
	m.backoff = 0
}

func (m *missedPolls) elapsed() time.Duration {
	return time.Since(m.started).Round(time.Millisecond)
}

// wait is how long to sleep before the next poll: poll after an answer,
// a growing backoff after a missed one.
func (m *missedPolls) wait(poll time.Duration) time.Duration {
	if m.streak > 0 && m.backoff > poll {
		return m.backoff
	}
	return poll
}

// timedOut is the loop's deadline error. It keeps base (callers match its
// wording, see updaterdeploy.ShouldRetryAfterScaleTimeout) and adds the
// loop, the elapsed time and, when polls failed, the last error.
func (m *missedPolls) timedOut(base string) error {
	if m.failed == 0 {
		return fmt.Errorf("%s (%s, after %s)", base, m.loop, m.elapsed())
	}
	return fmt.Errorf("%s (%s, after %s; %d %s polls failed, %d in a row; last error: %w)", base, m.loop, m.elapsed(), m.failed, m.call, m.streak, m.last)
}
