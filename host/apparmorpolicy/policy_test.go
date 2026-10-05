package apparmorpolicy

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

const runcExecEPERM = `container_linux.go:346: starting container process caused "process_linux.go:480: container init caused \"apply apparmor profile: apparmor failed to apply profile: write /proc/self/attr/exec: permission denied\""`

func TestIsApplyErr(t *testing.T) {
	if IsApplyErr(nil) {
		t.Fatal("nil")
	}
	if !IsApplyErr(errors.New(runcExecEPERM)) {
		t.Fatal("git-push runc wrapping must match")
	}
	if !IsApplyErr(errors.New("nsenter: failed to apply apparmor profile before user namespace: permission denied")) {
		t.Fatal("nsexec bail must match so confined retries still run")
	}
	if IsApplyErr(errors.New("file exists")) {
		t.Fatal("veth EEXIST is not AppArmor")
	}
}

func TestNextConfinedStartDelayCoversRestartWindow(t *testing.T) {
	var total time.Duration
	for i := 1; i < ConfinedStartAttempts; i++ {
		d, ok := NextConfinedStartDelay(i, ConfinedStartAttempts)
		if !ok {
			t.Fatalf("retry %d should still be confined", i)
		}
		total += d
	}
	if _, ok := NextConfinedStartDelay(ConfinedStartAttempts, ConfinedStartAttempts); ok {
		t.Fatal("no retry after max attempts")
	}
	if total < 12*time.Second {
		t.Fatalf("confined retries wait %s, want >= 12s so change_onexec can attach after daemon restart", total)
	}
}

func TestProfileForJobAttachesUserWorker(t *testing.T) {
	loaded := "flynn-default (enforce)\ndocker-default (enforce)\n"
	if got := ProfileForJob(false, true, loaded); got != ProfileName {
		t.Fatalf("user worker must get %s, got %q", ProfileName, got)
	}
	if got := ProfileForJob(false, false, loaded); got != "" {
		t.Fatal("kernel disabled")
	}
	if got := ProfileForJob(false, true, "docker-default (enforce)\n"); got != "" {
		t.Fatal("missing flynn-default")
	}
	if got := ProfileForJob(true, true, loaded); got != "" {
		t.Fatal("build jobs stay unconfined")
	}
}

func TestChangeOnExecQueued(t *testing.T) {
	if ChangeOnExecValue(ProfileName) != "exec flynn-default" {
		t.Fatal("payload")
	}
	if !ChangeOnExecQueued("exec flynn-default\n", ProfileName) {
		t.Fatal("queued after nsexec write")
	}
	if ChangeOnExecQueued("unconfined", ProfileName) {
		t.Fatal("wrong profile is not queued")
	}
	if ChangeOnExecQueued("", ProfileName) {
		t.Fatal("empty attr")
	}
}

func TestDecideStartNeverSkipsAppArmor(t *testing.T) {
	err := errors.New(runcExecEPERM)
	if got := DecideStart(ProfileName, err, 1, ConfinedStartAttempts); got != StartRetryConfined {
		t.Fatalf("first apply EPERM: %v", got)
	}
	if got := DecideStart(ProfileName, err, ConfinedStartAttempts, ConfinedStartAttempts); got != StartFail {
		t.Fatalf("exhausted retries must fail closed, got %v", got)
	}
}

func TestRuncQueuesAppArmorBeforeUserNamespace(t *testing.T) {
	nsexec, err := os.ReadFile("../../vendor/github.com/opencontainers/runc/libcontainer/nsenter/nsexec.c")
	if err != nil {
		t.Fatal(err)
	}
	body := string(nsexec)
	idxApply := strings.Index(body, "apparmor_change_onexec();")
	idxUnshare := strings.Index(body, "if (unshare(CLONE_NEWUSER)")
	if idxApply < 0 || idxUnshare < 0 || idxApply > idxUnshare {
		t.Fatal("nsexec must queue change_onexec before unshare(CLONE_NEWUSER)")
	}
	if !strings.Contains(body, ApparmorProfileEnv) {
		t.Fatal("nsexec must read " + ApparmorProfileEnv)
	}

	parent, err := os.ReadFile("../../vendor/github.com/opencontainers/runc/libcontainer/container_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(parent), ApparmorProfileEnv+"=") {
		t.Fatal("runc parent must pass the profile to nsexec")
	}

	aa, err := os.ReadFile("../../vendor/github.com/opencontainers/runc/libcontainer/apparmor/apparmor.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aa), "queuedChangeOnExec") {
		t.Fatal("ApplyProfile after NEWUSER must accept a profile already queued by nsexec")
	}
}

func TestLibcontainerKeepsAppArmorOnUserJobs(t *testing.T) {
	src, err := os.ReadFile("../libcontainer_backend.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if strings.Contains(body, "retrying without AppArmor") {
		t.Fatal("must not drop flynn-default when change_onexec fails")
	}
	if !strings.Contains(body, "apparmorpolicy.ProfileForJob") || !strings.Contains(body, "apparmorpolicy.DecideStart") {
		t.Fatal("container start must use the AppArmor helpers under test")
	}
	if strings.Contains(body, "CanChangeOnExec") {
		t.Fatal("must not skip the profile because /proc/self/attr/exec looks unwritable")
	}
}
