package backup

import (
	"io"
	"strings"
	"testing"
)

func TestSkipDumpallTemplateDatabases(t *testing.T) {
	in := strings.Join([]string{
		`-- PostgreSQL database cluster dump`,
		`DROP ROLE IF EXISTS flynn;`,
		`DROP ROLE IF EXISTS postgres;`,
		`CREATE ROLE flynn;`,
		`ALTER ROLE flynn WITH SUPERUSER LOGIN;`,
		`CREATE ROLE postgres;`,
		`ALTER ROLE postgres WITH SUPERUSER LOGIN;`,
		`-- Database "template1" dump`,
		`UPDATE pg_catalog.pg_database SET datistemplate = false WHERE datname = 'template1';`,
		`DROP DATABASE template1;`,
		`CREATE DATABASE template1 WITH TEMPLATE = template0 ENCODING = 'UTF8' LOCALE_PROVIDER = libc LOCALE = 'en_US.UTF-8';`,
		`ALTER DATABASE template1 OWNER TO postgres;`,
		`\connect template1`,
		`SELECT 1;`,
		`-- Database "postgres" dump`,
		`\connect postgres`,
		`CREATE TABLE keep_me (id int);`,
		`-- Database "0712cbc2fcb516506609d14ceabe6ec9" dump`,
		`\connect "0712cbc2fcb516506609d14ceabe6ec9"`,
		`CREATE TABLE app_data (id int);`,
		``,
	}, "\n")
	got, err := io.ReadAll(SkipDumpallTemplateDatabases(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	for _, banned := range []string{
		`DROP ROLE IF EXISTS flynn;`,
		`CREATE ROLE postgres;`,
		`-- Database "template1" dump`,
		`DROP DATABASE template1;`,
		`CREATE DATABASE template1 `,
		`\connect template1`,
	} {
		if strings.Contains(out, banned) {
			t.Fatalf("kept %q in:\n%s", banned, out)
		}
	}
	for _, want := range []string{
		`ALTER ROLE flynn WITH SUPERUSER LOGIN;`,
		`-- Database "postgres" dump`,
		`CREATE TABLE keep_me (id int);`,
		`-- Database "0712cbc2fcb516506609d14ceabe6ec9" dump`,
		`CREATE TABLE app_data (id int);`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
