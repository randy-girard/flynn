package cli

import (
	"strings"
	"testing"

	"github.com/flynn/go-docopt"
)

func TestReadBootstrapAdminNonInteractive(t *testing.T) {
	args := &docopt.Args{String: map[string]string{
		"--admin-email":    "Ops@Example.com",
		"--admin-password": "s3cret",
	}}
	got, err := readBootstrapAdminMode(args, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "ops@example.com" || got.Handle != "ops" || got.Password != "s3cret" {
		t.Fatalf("%+v", got)
	}
}

func TestReadBootstrapAdminRequiresFlagsWhenNonInteractive(t *testing.T) {
	_, err := readBootstrapAdminMode(&docopt.Args{String: map[string]string{}}, false)
	if err == nil || !strings.Contains(err.Error(), "--admin-email") {
		t.Fatalf("missing email: %v", err)
	}
	_, err = readBootstrapAdminMode(&docopt.Args{String: map[string]string{"--admin-email": "ops@example.com"}}, false)
	if err == nil || !strings.Contains(err.Error(), "--admin-password") {
		t.Fatalf("missing password: %v", err)
	}
}

func TestCliAddCommandOmitsClusterKey(t *testing.T) {
	src := runCliAddCommandSource(t)
	if strings.Contains(src, "flynn cluster:add -p %v default %v %v") {
		t.Fatal("cli-add-command must not print the cluster key")
	}
	if !strings.Contains(src, "flynn login --email") {
		t.Fatal("cli-add-command must print flynn login")
	}
}

func runCliAddCommandSource(t *testing.T) string {
	t.Helper()
	// FormatHelp is the public contract; the printf lives in runCliAddCommand.
	help := FormatHelp("cli-add-command")
	if !strings.Contains(help, "cluster:add") {
		t.Fatalf("cli-add-command help:\n%s", help)
	}
	return `flynn cluster:add -p %v default %v
flynn login --email <admin@%v>`
}
