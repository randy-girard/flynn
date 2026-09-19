package githubapp

import "strings"

// CheckState is the combined GitHub Checks + commit-status result for a SHA.
type CheckState string

const (
	CheckPending CheckState = "pending"
	CheckPassed  CheckState = "passed"
	CheckFailed  CheckState = "failed"
)

// CheckRun is one GitHub Checks run.
type CheckRun struct {
	Status     string
	Conclusion string
}

// CommitStatus is one legacy commit status plus the combined rollup.
type CommitStatus struct {
	State string
}

// EvaluateChecks implements Heroku-style "wait for CI":
// pending while any check/status is in progress; failed if any failed;
// passed when everything completed successfully. Zero checks with a completed
// check suite (or an explicit empty+completed flag) counts as passed so repos
// without CI can still auto-deploy after GitHub's empty suite event.
func EvaluateChecks(runs []CheckRun, statuses []CommitStatus, combinedState string, emptySuiteCompleted bool) CheckState {
	combinedState = strings.ToLower(strings.TrimSpace(combinedState))
	if combinedState == "failure" || combinedState == "error" {
		return CheckFailed
	}
	for _, s := range statuses {
		switch strings.ToLower(s.State) {
		case "failure", "error":
			return CheckFailed
		case "pending":
			return CheckPending
		}
	}
	for _, r := range runs {
		st := strings.ToLower(r.Status)
		conc := strings.ToLower(r.Conclusion)
		if st == "queued" || st == "in_progress" || st == "waiting" || st == "pending" {
			return CheckPending
		}
		if st == "completed" {
			switch conc {
			case "failure", "cancelled", "timed_out", "action_required", "stale", "startup_failure":
				return CheckFailed
			}
		}
	}
	if combinedState == "pending" {
		return CheckPending
	}
	if len(runs) == 0 && len(statuses) == 0 && combinedState == "" {
		if emptySuiteCompleted {
			return CheckPassed
		}
		return CheckPending
	}
	return CheckPassed
}
