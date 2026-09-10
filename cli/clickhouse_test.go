package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestClickhouseAdmin(t *testing.T) {
	got := clickhouseAdmin("--query", "SELECT 1")
	want := []string{"/bin/flynn-clickhouse", "admin", "clickhouse-client", "--query", "SELECT 1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestClickhouseArgsHaveQuery(t *testing.T) {
	if !clickhouseArgsHaveQuery([]string{"--query", "SELECT 1"}) {
		t.Fatal("expected --query to be detected")
	}
	if !clickhouseArgsHaveQuery([]string{"--query=SELECT 1"}) {
		t.Fatal("expected --query= to be detected")
	}
	if clickhouseArgsHaveQuery([]string{"--host", "leader.clickhouse.discoverd"}) {
		t.Fatal("interactive client args must not look like --query")
	}
}

func TestShouldCloseClickhouseStdin(t *testing.T) {
	insertValues := clickhouseAdmin("--query", "INSERT INTO smoke_db.rows VALUES (1, 'pre-upgrade')")
	insertEq := clickhouseAdmin("--query=INSERT INTO t VALUES (1)")
	sel := clickhouseAdmin("--query", "SELECT count() FROM smoke_db.rows")
	interactive := clickhouseAdmin()
	hostOnly := []string{"clickhouse-client", "--host", "leader.clickhouse.discoverd"}

	cases := []struct {
		name            string
		args            []string
		stdinAlreadySet bool
		stdinIsTTY      bool
		want            bool
	}{
		{"tty INSERT VALUES", insertValues, false, true, true},
		{"tty --query= INSERT VALUES", insertEq, false, true, true},
		{"tty SELECT", sel, false, true, true},
		{"pipe INSERT VALUES", insertValues, false, false, false},
		{"explicit stdin keeps pipe for FORMAT CSV", insertValues, true, true, false},
		{"interactive client keeps TTY", interactive, false, true, false},
		{"no --query flag", hostOnly, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := shouldCloseClickhouseStdin(c.args, c.stdinAlreadySet, c.stdinIsTTY)
			if got != c.want {
				t.Fatalf("shouldCloseClickhouseStdin(%v, set=%v, tty=%v) = %v, want %v",
					c.args, c.stdinAlreadySet, c.stdinIsTTY, got, c.want)
			}
		})
	}
}

func TestApplyClickhouseStdinPolicy(t *testing.T) {
	insert := clickhouseAdmin("--query", "INSERT INTO smoke_db.rows VALUES (1, 'pre-upgrade')")

	ttyQuery := &runConfig{Args: insert}
	applyClickhouseStdinPolicy(ttyQuery, true)
	if ttyQuery.Stdin == nil {
		t.Fatal("TTY --query must replace stdin so flynn run CloseWrite()s instead of hanging on the TTY")
	}
	body, err := io.ReadAll(ttyQuery.Stdin)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("closed stdin must be empty, got %q", body)
	}

	piped := &runConfig{Args: insert}
	applyClickhouseStdinPolicy(piped, false)
	if piped.Stdin != nil {
		t.Fatal("piped INSERT FORMAT CSV must keep the default stdin (os.Stdin)")
	}

	interactive := &runConfig{Args: clickhouseAdmin()}
	applyClickhouseStdinPolicy(interactive, true)
	if interactive.Stdin != nil {
		t.Fatal("interactive clickhouse client must keep the TTY")
	}

	preset := strings.NewReader("keep-me")
	explicit := &runConfig{Args: insert, Stdin: preset}
	applyClickhouseStdinPolicy(explicit, true)
	if explicit.Stdin != preset {
		t.Fatal("an already-set stdin must not be replaced")
	}
}

func TestCreateDatabaseQuery(t *testing.T) {
	got := createDatabaseQuery("analytics", "flynn")
	want := "CREATE DATABASE IF NOT EXISTS `analytics` ON CLUSTER `flynn` ENGINE = Atomic"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDropDatabaseQuery(t *testing.T) {
	got := dropDatabaseQuery("analytics", "flynn")
	want := "DROP DATABASE IF EXISTS `analytics` ON CLUSTER `flynn` SYNC"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEscapeClickhouseIdentifier(t *testing.T) {
	if got, want := escapeClickhouseIdentifier("analytics"), "analytics"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := escapeClickhouseIdentifier("a`b"), "a``b"; got != want {
		t.Fatalf("backtick escape: got %q, want %q", got, want)
	}
	got := createDatabaseQuery("a`b", "flynn")
	if !strings.Contains(got, "`a``b`") || !strings.Contains(got, "ON CLUSTER `flynn`") {
		t.Fatalf("ON CLUSTER query must escape identifiers: %q", got)
	}
}
