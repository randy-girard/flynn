package main

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsErrDeadlineExceededUnknownWrap(t *testing.T) {
	err := status.Error(codes.Unknown, context.DeadlineExceeded.Error())
	if !isErrDeadlineExceeded(err) {
		t.Fatalf("Unknown+deadline should be treated as deadline exceeded, got %#v", err)
	}
	if isErrCanceled(err) {
		t.Fatal("deadline wrap should not be treated as canceled")
	}
}

func TestIsErrCanceledUnknownWrap(t *testing.T) {
	err := status.Error(codes.Unknown, context.Canceled.Error())
	if !isErrCanceled(err) {
		t.Fatalf("Unknown+canceled should be treated as canceled, got %#v", err)
	}
	if isErrDeadlineExceeded(err) {
		t.Fatal("canceled wrap should not be treated as deadline exceeded")
	}
}
