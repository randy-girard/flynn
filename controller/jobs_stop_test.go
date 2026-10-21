package main

import (
	"errors"
	"fmt"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
	"github.com/randy-girard/flynn/pkg/httphelper"
)

func TestHostStopFinished(t *testing.T) {
	if hostStopFinished(nil) {
		t.Fatal("nil stop error is not a finished job")
	}
	if hostStopFinished(errors.New("connection refused")) {
		t.Fatal("a host that failed to stop a live job is not finished")
	}
	if hostStopFinished(fmt.Errorf("unknown_error: Something went wrong")) {
		t.Fatal("a generic host failure must not be treated as the job being gone")
	}
	if !hostStopFinished(ct.NotFoundError{Resource: "job"}) {
		t.Fatal("typed not found")
	}
	if !hostStopFinished(httphelper.JSONError{Code: httphelper.ObjectNotFoundErrorCode, Message: "host: unknown job"}) {
		t.Fatal("object not found")
	}
	if !hostStopFinished(errors.New("host: unknown job")) {
		t.Fatal("unknown job")
	}
	if !hostStopFinished(errors.New("host: job is already stopped")) {
		t.Fatal("already stopped")
	}
}
