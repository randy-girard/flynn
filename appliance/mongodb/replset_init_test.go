package mongodb

import (
	"errors"
	"testing"

	discoverd "github.com/flynn/flynn/discoverd/client"
	"github.com/flynn/flynn/pkg/sirenia/state"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestIsMongoError(t *testing.T) {
	err := mongo.CommandError{Code: 23, Message: "already initialized"}
	if !isMongoError(err, 23) {
		t.Fatal("expected AlreadyInitialized to match code 23")
	}
	if isMongoError(err, 94) {
		t.Fatal("did not expect code 94 match")
	}
	if isMongoError(errors.New("other"), 23) {
		t.Fatal("non-command error should not match")
	}
}

func TestReplSetConfigFromStatePreservesPrimaryIDOnHostChange(t *testing.T) {
	p := NewProcess()
	current := &replSetConfig{
		ID: "rs0",
		Members: []replSetMember{
			{ID: 0, Host: "100.100.83.103:27017", Priority: 1},
			{ID: 2, Host: "100.100.60.3:27017", Priority: 0},
		},
		Version: 5,
	}
	clusterState := &state.State{
		Primary: &discoverd.Instance{Addr: "100.100.83.104:27017"},
		Sync:    &discoverd.Instance{Addr: "100.100.60.3:27017"},
	}
	got := p.replSetConfigFromState(current, clusterState)
	if len(got.Members) != 2 {
		t.Fatalf("members=%d want 2", len(got.Members))
	}
	if got.Members[0].ID != 0 || got.Members[0].Host != "100.100.83.104:27017" {
		t.Fatalf("primary member=%+v", got.Members[0])
	}
	if got.Members[1].ID != 2 {
		t.Fatalf("sync id=%d want 2", got.Members[1].ID)
	}
	if got.Version != 6 {
		t.Fatalf("version=%d want 6", got.Version)
	}
}

func TestReplSetConfiguredFromStatusError(t *testing.T) {
	cases := []struct {
		code       int
		configured bool
		err        bool
	}{
		{0, true, false},
		{93, true, false},
		{13436, true, false},
		{94, false, false},
		{1, false, true},
	}
	for _, tc := range cases {
		var err error
		if tc.code == 0 {
			err = nil
		} else {
			err = mongo.CommandError{Code: int32(tc.code)}
		}
		configured, checkErr := replSetConfiguredFromStatusError(err)
		if configured != tc.configured {
			t.Fatalf("code %d: configured=%v want %v", tc.code, configured, tc.configured)
		}
		if (checkErr != nil) != tc.err {
			t.Fatalf("code %d: err=%v want err=%v", tc.code, checkErr, tc.err)
		}
	}
}
