package data

import (
	"os"
	"strings"
	"testing"
)

func TestQueuedDeploysReplaceIsolateDeploysBlock(t *testing.T) {
	b, err := os.ReadFile("deployment.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, `status = "queued"`) {
		t.Fatal("a second deploy must queue instead of failing isolate_deploys")
	}
	if !strings.Contains(src, "txLatestUnfinished") {
		t.Fatal("stacked deploys must parent from the newest unfinished new_release")
	}
	q, err := os.ReadFile("deployment_queue.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(q), "StartNextQueued") {
		t.Fatal("finishing a deploy must start the next queued job")
	}
}

func TestEnvOnlyReleaseCanDeployBeforeFirstGitPush(t *testing.T) {
	b, err := os.ReadFile("deployment.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "len(release.ArtifactIDs) > 0 && !release.HasDeployableBlob()") {
		t.Fatal("env-only attach (no artifacts) must not be treated as a garbage-collected blob")
	}
	if !strings.Contains(src, `len(release.ArtifactIDs) == 0`) {
		t.Fatal("a release with no artifacts is a config deploy so resource attach works before git push")
	}
}
