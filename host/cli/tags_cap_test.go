package cli

import (
	"testing"

	ct "github.com/randy-girard/flynn/controller/types"
)

func TestClassifySetSingletonCap(t *testing.T) {
	tags := map[string]string{ct.FormationMaxPersistentSingletonsTag: "unlimited", "role": "db"}
	classifySetSingletonCap(tags)
	if tags[ct.FormationMaxPersistentSingletonsTag] != "" || tags["role"] != "db" {
		t.Fatalf("sentinel must delete the cap, tags %v", tags)
	}
	tags = map[string]string{ct.FormationMaxPersistentSingletonsTag: " 3 "}
	classifySetSingletonCap(tags)
	if tags[ct.FormationMaxPersistentSingletonsTag] != "3" {
		t.Fatalf("number %q", tags[ct.FormationMaxPersistentSingletonsTag])
	}
	tags = map[string]string{ct.FormationMaxPersistentSingletonsTag: "lots"}
	classifySetSingletonCap(tags)
	if tags[ct.FormationMaxPersistentSingletonsTag] != "lots" {
		t.Fatalf("non-integer %q", tags[ct.FormationMaxPersistentSingletonsTag])
	}
}
