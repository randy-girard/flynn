package backup

import (
	"bufio"
	"io"
	"strings"
)

// SkipDumpallTemplateDatabases strips template0/template1 from a pg_dumpall
// stream. DROP/CREATE of those databases on a freshly started Flynn postgres
// races catalog updates ("tuple concurrently updated") and leaves template1
// invalid so the following \connect fails.
func SkipDumpallTemplateDatabases(r io.Reader) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		err := filterDumpallTemplateDatabases(r, pw)
		pw.CloseWithError(err)
	}()
	return pr
}

func filterDumpallTemplateDatabases(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	skipping := false
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			if !keepDumpallLine(string(line), &skipping) {
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				continue
			}
			if _, werr := w.Write(line); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func keepDumpallLine(line string, skipping *bool) bool {
	trimmed := strings.TrimRight(line, "\r\n")
	if isDumpallDatabaseHeader(trimmed) {
		*skipping = dumpallTemplateDatabase(trimmed)
		return !*skipping
	}
	if *skipping {
		return false
	}
	return !isDumpallTemplateCatalogLine(trimmed)
}

func isDumpallDatabaseHeader(line string) bool {
	return strings.HasPrefix(line, `-- Database "`) && strings.HasSuffix(line, `" dump`)
}

func dumpallTemplateDatabase(header string) bool {
	name := strings.TrimSuffix(strings.TrimPrefix(header, `-- Database "`), `" dump`)
	return name == "template0" || name == "template1"
}

func isDumpallTemplateCatalogLine(line string) bool {
	switch {
	case line == `DROP ROLE IF EXISTS flynn;` || line == `DROP ROLE IF EXISTS postgres;`:
		return true
	case line == `CREATE ROLE flynn;` || line == `CREATE ROLE postgres;`:
		return true
	case strings.HasPrefix(line, `UPDATE pg_catalog.pg_database SET datistemplate = false WHERE datname = 'template1'`):
		return true
	case strings.HasPrefix(line, `UPDATE pg_catalog.pg_database SET datistemplate = false WHERE datname = 'template0'`):
		return true
	case line == `DROP DATABASE template1;` || line == `DROP DATABASE template0;`:
		return true
	case line == `DROP DATABASE IF EXISTS template1;` || line == `DROP DATABASE IF EXISTS template0;`:
		return true
	case strings.HasPrefix(line, `CREATE DATABASE template1 `) || strings.HasPrefix(line, `CREATE DATABASE template0 `):
		return true
	case strings.HasPrefix(line, `ALTER DATABASE template1 `) || strings.HasPrefix(line, `ALTER DATABASE template0 `):
		return true
	case line == `\connect template1` || line == `\connect template0`:
		return true
	default:
		return false
	}
}
