package discoverd

import (
	"testing"

	discoverd "github.com/randy-girard/flynn/discoverd/client"
	hh "github.com/randy-girard/flynn/pkg/httphelper"
)

func TestMetaAbsent(t *testing.T) {
	gone := hh.JSONError{Code: hh.ObjectNotFoundErrorCode, Message: "service meta not found"}
	if !metaAbsent(gone, nil) {
		t.Fatal("missing meta should be republished")
	}
	if metaAbsent(hh.JSONError{Code: hh.ServiceUnavailableErrorCode, Message: "down"}, nil) {
		t.Fatal("a discoverd outage is not missing meta")
	}
	if metaAbsent(nil, &discoverd.ServiceMeta{Data: []byte(`{"generation":1}`)}) {
		t.Fatal("existing meta must be left in place")
	}
	if !metaAbsent(nil, &discoverd.ServiceMeta{}) {
		t.Fatal("empty meta should be republished")
	}
}
