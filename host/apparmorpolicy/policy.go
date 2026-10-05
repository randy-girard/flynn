package apparmorpolicy

import (
	"strings"
	"time"
)

const ProfileName = "flynn-default"
const ApparmorProfileEnv = "_LIBCONTAINER_APPARMOR_PROFILE"

// ConfinedStartAttempts is how many confined starts to try. The LSM can
// refuse change_onexec for a few seconds after flynn-host restarts; these
// retries exist to attach flynn-default, not to skip it.
const ConfinedStartAttempts = 6

var ConfinedStartBackoffs = []time.Duration{
	250 * time.Millisecond,
	time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
}

type StartAction int

const (
	StartFail StartAction = iota
	StartRetryConfined
)

func IsApplyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "apparmor") || strings.Contains(msg, "apply apparmor")
}

func NextConfinedStartDelay(failureCount, maxAttempts int) (time.Duration, bool) {
	if failureCount <= 0 || failureCount >= maxAttempts {
		return 0, false
	}
	idx := failureCount - 1
	if idx >= len(ConfinedStartBackoffs) {
		return ConfinedStartBackoffs[len(ConfinedStartBackoffs)-1], true
	}
	return ConfinedStartBackoffs[idx], true
}

// ProfileForJob is flynn-default for non-build jobs when the kernel LSM is
// on and the profile is loaded. User jobs keep the profile: runc must queue
// change_onexec before NEWUSER so attach succeeds instead of skipping MAC.
func ProfileForJob(isBuild, kernelEnabled bool, loadedProfiles string) string {
	if isBuild || !kernelEnabled {
		return ""
	}
	if !strings.Contains(loadedProfiles, ProfileName) {
		return ""
	}
	return ProfileName
}

// ChangeOnExecValue is the payload written to /proc/self/attr/exec.
func ChangeOnExecValue(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return ""
	}
	return "exec " + profile
}

// ChangeOnExecQueued reports that change_onexec already lists profile, so a
// later EPERM write after NEWUSER is not a failed attach.
func ChangeOnExecQueued(attr, profile string) bool {
	want := ChangeOnExecValue(profile)
	if want == "" {
		return false
	}
	got := strings.TrimSpace(attr)
	return got == want || strings.HasPrefix(got, want)
}

// DecideStart retries confined starts on apply EPERM. It never falls back
// to unconfined: failing to attach flynn-default must fail the job.
func DecideStart(profile string, err error, confinedFailures, maxConfined int) StartAction {
	if err == nil || profile == "" || !IsApplyErr(err) {
		return StartFail
	}
	if _, ok := NextConfinedStartDelay(confinedFailures, maxConfined); ok {
		return StartRetryConfined
	}
	return StartFail
}
