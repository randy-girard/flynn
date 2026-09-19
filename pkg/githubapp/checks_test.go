package githubapp

import "testing"

func TestEvaluateChecks(t *testing.T) {
	cases := []struct {
		name     string
		runs     []CheckRun
		status   []CommitStatus
		combined string
		emptyOK  bool
		want     CheckState
	}{
		{"pending run", []CheckRun{{Status: "in_progress"}}, nil, "", false, CheckPending},
		{"failed run", []CheckRun{{Status: "completed", Conclusion: "failure"}}, nil, "", false, CheckFailed},
		{"failed status", nil, []CommitStatus{{State: "failure"}}, "failure", false, CheckFailed},
		{"all success", []CheckRun{{Status: "completed", Conclusion: "success"}}, []CommitStatus{{State: "success"}}, "success", false, CheckPassed},
		{"empty pending", nil, nil, "", false, CheckPending},
		{"empty suite completed", nil, nil, "", true, CheckPassed},
		{"neutral check counts as pass", []CheckRun{{Status: "completed", Conclusion: "neutral"}}, nil, "success", false, CheckPassed},
		{"cancelled is fail", []CheckRun{{Status: "completed", Conclusion: "cancelled"}}, nil, "", false, CheckFailed},
		{"combined pending", []CheckRun{{Status: "completed", Conclusion: "success"}}, nil, "pending", false, CheckPending},
	}
	for _, tc := range cases {
		got := EvaluateChecks(tc.runs, tc.status, tc.combined, tc.emptyOK)
		if got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.name, got, tc.want)
		}
	}
}
