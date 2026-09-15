package mysqlurl

import (
	"strings"
	"testing"
	"time"
)

func TestDSNString(t *testing.T) {
	got := (&DSN{
		Host:     "leader.mariadb.discoverd:3306",
		User:     "flynn",
		Password: "secret",
		Database: "mysql",
		Timeout:  30 * time.Second,
	}).String()
	for _, want := range []string{
		"flynn:secret",
		"tcp(leader.mariadb.discoverd:3306)",
		"/mysql",
		"timeout=30s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dsn %q missing %q", got, want)
		}
	}
}

func TestDSNStringNoPassword(t *testing.T) {
	got := (&DSN{Host: "127.0.0.1:3306", User: "root", Database: "db"}).String()
	if strings.Contains(got, ":@") || !strings.Contains(got, "root@") {
		t.Fatalf("empty password: %q", got)
	}
}
