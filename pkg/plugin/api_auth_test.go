package plugin

import (
	"reflect"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestAPIAuthEnvCopiesOnlyThePluginKeys(t *testing.T) {
	rel := &ct.Release{Env: map[string]string{
		"CONTROLLER_KEY":     " plugin-key ",
		"AUTH_KEY":           "auth-key",
		"DATABASE_URL":       "postgres://secret",
		"DISCOVERD_AUTH_KEY": "d",
	}}
	got := APIAuthEnv(rel)
	want := map[string]string{"CONTROLLER_KEY": "plugin-key", "AUTH_KEY": "auth-key"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := APIAuthEnv(nil); len(got) != 0 {
		t.Fatalf("nil release: %v", got)
	}
	if got := APIAuthEnv(&ct.Release{Env: map[string]string{"CONTROLLER_KEY": " "}}); len(got) != 0 {
		t.Fatalf("blank key: %v", got)
	}
}

func TestAPIBinary(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"/bin/flynn-kafka-api", "task", "maintenance"}, true},
		{[]string{"/bin/flynn-mongodb-api", "maintenance"}, true},
		{[]string{"/bin/flynn-mysql-api"}, true},
		{[]string{"/bin/flynn-kafka-cli", "topics"}, false},
		{[]string{"psql", "flynn-x-api"}, false},
		{nil, false},
	} {
		if got := APIBinary(tc.args); got != tc.want {
			t.Errorf("APIBinary(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
