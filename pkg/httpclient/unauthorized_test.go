package httpclient

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsUnauthorized(t *testing.T) {
	if IsUnauthorized(nil) {
		t.Fatal("nil")
	}
	if IsUnauthorized(errors.New("blobstore unavailable")) {
		t.Fatal("transient")
	}
	err := fmt.Errorf(`POST "http://controller.discoverd/artifacts": httpclient: raw req: unexpected status 401`)
	if !IsUnauthorized(err) {
		t.Fatal("401")
	}
}
