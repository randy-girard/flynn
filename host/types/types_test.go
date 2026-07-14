package host

import (
	"testing"
)

// TestMergeSecrets verifies that ContainerConfig.Merge combines the secret
// files from both configs (like Mounts/Volumes/Ports), so a base config and an
// overlay each contributing a secret both survive the merge.
func TestMergeSecrets(t *testing.T) {
	base := ContainerConfig{
		Secrets: []ContainerSecret{
			{Path: "/run/secrets/a", Data: []byte("aaa")},
		},
	}
	overlay := ContainerConfig{
		Secrets: []ContainerSecret{
			{Path: "/run/secrets/b", Data: []byte("bbb")},
		},
	}

	merged := base.Merge(overlay)
	if len(merged.Secrets) != 2 {
		t.Fatalf("merged secrets = %d, want 2: %#v", len(merged.Secrets), merged.Secrets)
	}
	got := map[string]string{}
	for _, s := range merged.Secrets {
		got[s.Path] = string(s.Data)
	}
	if got["/run/secrets/a"] != "aaa" || got["/run/secrets/b"] != "bbb" {
		t.Fatalf("merged secrets content mismatch: %#v", got)
	}
}

// TestMergeSecretsEmpty verifies merging when neither side has secrets yields a
// non-nil, empty slice (consistent with the other merged slice fields).
func TestMergeSecretsEmpty(t *testing.T) {
	merged := ContainerConfig{}.Merge(ContainerConfig{})
	if len(merged.Secrets) != 0 {
		t.Fatalf("expected no secrets, got %#v", merged.Secrets)
	}
}
