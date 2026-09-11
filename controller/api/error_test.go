package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	controller "github.com/flynn/flynn/controller/client"
	ct "github.com/flynn/flynn/controller/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"canceled", context.Canceled, codes.Canceled},
		{"wrapped deadline", fmt.Errorf("wait: %w", context.DeadlineExceeded), codes.DeadlineExceeded},
		{"wrapped canceled", fmt.Errorf("wait: %w", context.Canceled), codes.Canceled},
		{"not found", controller.ErrNotFound, codes.NotFound},
		{"validation", ct.ValidationError{Message: "bad"}, codes.InvalidArgument},
		{"other", errors.New("boom"), codes.Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := status.Code(NewError(tt.err, "%s", tt.err.Error()))
			if got != tt.want {
				t.Fatalf("NewError(%v) code = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
