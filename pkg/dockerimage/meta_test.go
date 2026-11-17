package dockerimage

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestArtifactMeta(t *testing.T) {
	meta := ArtifactMeta(&BuildResult{ListenPort: 9090})
	if meta[MetaListenPort] != "9090" {
		t.Fatalf("meta = %#v", meta)
	}
	if _, ok := meta[MetaEntrypoint]; ok {
		t.Fatal("missing image config must not invent an entrypoint")
	}
	if ArtifactMeta(nil) != nil {
		t.Fatal("expected nil meta")
	}
	cfg := &Config{}
	cfg.Config.Entrypoint = []string{"/entry"}
	cfg.Config.Cmd = []string{"web"}
	withCmd := ArtifactMeta(&BuildResult{ListenPort: 8080, Config: cfg})
	if withCmd[MetaEntrypoint] != `["/entry"]` || withCmd[MetaCmd] != `["web"]` {
		t.Fatalf("command meta %#v", withCmd)
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
