package main

import "testing"

func TestUserCLIDoesNotRegisterPg(t *testing.T) {
	for _, name := range []string{"pg", "pg:psql", "pg:dump", "pg:restore"} {
		if commands[name] != nil {
			t.Fatalf("%s is not a flynn command; tenant pg is the postgres plugin and the platform database is flynn-host pg", name)
		}
	}
	if _, ok := subAliases["pg"]; ok {
		t.Fatal("flynn pg space alias must not resolve to a built-in command")
	}
}
