package dockerimage

import (
	"testing"

	ct "github.com/flynn/flynn/controller/types"
)

func TestArtifactMeta(t *testing.T) {
	meta := ArtifactMeta(&BuildResult{ListenPort: 9090})
	if meta[MetaListenPort] != "9090" {
		t.Fatalf("meta = %#v", meta)
	}
	if ArtifactMeta(nil) != nil {
		t.Fatal("expected nil meta")
	}
}

func TestListenPortFromArtifact(t *testing.T) {
	artifact := &ct.Artifact{
		Meta: map[string]string{MetaListenPort: "4567"},
	}
	if got := ListenPortFromArtifact(artifact); got != 4567 {
		t.Fatalf("got %d", got)
	}
	if got := ListenPortFromArtifact(nil); got != 8080 {
		t.Fatalf("got %d", got)
	}
}
