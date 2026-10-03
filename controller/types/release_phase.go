package types

import (
	"fmt"
	"strings"
)

// IsReleaseProcessType reports a Procfile `release` process: run once after
// the new release is created and before traffic is swapped, then shut down.
func IsReleaseProcessType(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), ProcessTypeRelease)
}

// ProcessDisplayCommand is the command shown in the dashboard Resources tab
// and git-push output. Prefer the stored Procfile/Docker command; fall back
// to joining Args.
func ProcessDisplayCommand(p ProcessType) string {
	if c := strings.TrimSpace(p.Command); c != "" {
		return c
	}
	if len(p.Args) == 0 {
		return ""
	}
	if len(p.Args) >= 3 && p.Args[0] == "/runner/init" && p.Args[1] == "start" {
		return strings.Join(p.Args[2:], " ")
	}
	return strings.Join(p.Args, " ")
}

// WithoutReleaseProcessCounts drops `release` from a formation so the
// scheduler never keeps a release dyno running.
func WithoutReleaseProcessCounts(in map[string]int) map[string]int {
	if in == nil {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		if IsReleaseProcessType(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// ReleasePhaseNewJob is the one-off job that runs a release's `release`
// process type. Nil when the release has no such type (or no args to run).
func ReleasePhaseNewJob(release *Release) *NewJob {
	if release == nil {
		return nil
	}
	proc, ok := release.Processes[ProcessTypeRelease]
	if !ok || len(proc.Args) == 0 {
		return nil
	}
	return &NewJob{
		ReleaseID:  release.ID,
		ReleaseEnv: true,
		Type:       ProcessTypeRelease,
		Args:       append([]string{}, proc.Args...),
		Resources:  proc.Resources,
		Profiles:   proc.Profiles,
		MountsFrom: ProcessTypeRelease,
	}
}

// ReleasePhaseCompleted reports a successful `release` job for this release
// (gitreceive already ran it before DeployAppRelease).
func ReleasePhaseCompleted(jobs []*Job, releaseID string) bool {
	if releaseID == "" {
		return false
	}
	for _, j := range jobs {
		if j == nil || j.ReleaseID != releaseID || !IsReleaseProcessType(j.Type) {
			continue
		}
		if j.State == JobStateDown && (j.ExitStatus == nil || *j.ExitStatus == 0) {
			return true
		}
	}
	return false
}

// ReleasePhaseExitError is a non-zero exit from the release command.
func ReleasePhaseExitError(exitStatus int) error {
	return fmt.Errorf("release command failed with exit status %d", exitStatus)
}
