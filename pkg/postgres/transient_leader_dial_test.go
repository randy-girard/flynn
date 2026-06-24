package postgres

import (
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx"
)

func TestIsTransientLeaderDialErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New(`dial tcp: lookup leader.postgres.discoverd on 100.100.58.1:53: no such host`), true},
		{errors.New("connection refused"), true},
		{pgx.ErrDeadConn, true},
		{io.EOF, true},
		{errors.New(`ERROR: syntax error`), false},
	}
	for _, tc := range cases {
		got := IsTransientLeaderDialErr(tc.err)
		if got != tc.want {
			t.Fatalf("transient(%v): got %v want %v", tc.err, got, tc.want)
		}
	}
}
