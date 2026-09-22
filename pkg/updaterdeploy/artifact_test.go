package updaterdeploy

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

type stubArtifactClient struct {
	calls int
	err   error
}

func (s *stubArtifactClient) CreateArtifact(*ct.Artifact) error {
	s.calls++
	return s.err
}

func TestCreateArtifactWithRetryDoesNotRetry401(t *testing.T) {
	prev := artifactRetryDelay
	artifactRetryDelay = 0
	defer func() { artifactRetryDelay = prev }()

	stub := &stubArtifactClient{err: fmt.Errorf(`POST "http://controller.discoverd/artifacts": httpclient: raw req: unexpected status 401`)}
	err := CreateArtifactWithRetry(stub, "slugrunner", &ct.Artifact{ID: "sr"}, nil)
	if err == nil {
		t.Fatal("expected 401 to fail")
	}
	if stub.calls != 1 {
		t.Fatalf("401 retried %d times, want 1", stub.calls)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("err=%v", err)
	}
}

func TestCreateArtifactWithRetryRetriesTransient(t *testing.T) {
	prev := artifactRetryDelay
	artifactRetryDelay = 0
	defer func() { artifactRetryDelay = prev }()

	stub := &stubArtifactClient{err: errors.New("blobstore unavailable")}
	err := CreateArtifactWithRetry(stub, "slugrunner", &ct.Artifact{ID: "sr"}, nil)
	if err == nil {
		t.Fatal("expected retries to exhaust")
	}
	if stub.calls != 6 {
		t.Fatalf("transient errors retried %d times, want 6", stub.calls)
	}
}
