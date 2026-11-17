package cli

import (
	"bytes"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestApplyFlannelRestoreEnv(t *testing.T) {
	t.Setenv("FLANNEL_NETWORK", "")
	t.Setenv("FLANNEL_SUBNET_LEN", "")
	t.Setenv("FLANNEL_SUBNET_MIN", "")
	t.Setenv("FLANNEL_SUBNET_MAX", "")

	app := &ct.ExpandedFormation{Release: &ct.Release{Env: map[string]string{}}}
	var buf bytes.Buffer
	if err := applyFlannelRestoreEnv(app, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 || len(app.Release.Env) != 0 {
		t.Fatalf("empty env changed the release: %q %#v", buf.String(), app.Release.Env)
	}

	t.Setenv("FLANNEL_NETWORK", "100.64.0.0/10")
	t.Setenv("FLANNEL_SUBNET_LEN", "24")
	t.Setenv("FLANNEL_SUBNET_MIN", "100.64.1.0")
	t.Setenv("FLANNEL_SUBNET_MAX", "100.127.255.0")
	buf.Reset()
	if err := applyFlannelRestoreEnv(app, &buf); err != nil {
		t.Fatal(err)
	}
	if app.Release.Env["NETWORK"] != "100.64.0.0/10" || app.Release.Env["SUBNET_LEN"] != "24" {
		t.Fatalf("env: %#v", app.Release.Env)
	}
	sql := buf.String()
	for _, want := range []string{
		"'NETWORK', '100.64.0.0/10'",
		"'SUBNET_LEN', '24'",
		"'SUBNET_MIN', '100.64.1.0'",
		"'SUBNET_MAX', '100.127.255.0'",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("sql missing %s:\n%s", want, sql)
		}
	}

	t.Setenv("FLANNEL_NETWORK", "100.64.0.0/10'; DROP TABLE releases; --")
	if err := applyFlannelRestoreEnv(app, &buf); err == nil {
		t.Fatal("quoted network must be rejected")
	}
	t.Setenv("FLANNEL_NETWORK", "not-a-cidr")
	if err := applyFlannelRestoreEnv(app, &buf); err == nil {
		t.Fatal("bad network must be rejected")
	}
	t.Setenv("FLANNEL_NETWORK", "")
	t.Setenv("FLANNEL_SUBNET_LEN", "0")
	if err := applyFlannelRestoreEnv(app, &buf); err == nil {
		t.Fatal("subnet length 0 must be rejected")
	}
}
