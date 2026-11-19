package verdict

import "testing"

func TestExitCodes(t *testing.T) {
	cases := []struct {
		state State
		exit  int
		rank  int
	}{
		{Proven, 0, 0},
		{Passed, 0, 1},
		{Blocked, 1, 3},
		{NotChecked, 2, 2},
	}
	for _, tc := range cases {
		if got := tc.state.ExitCode(); got != tc.exit {
			t.Fatalf("%s exit: got %d want %d", tc.state, got, tc.exit)
		}
		if got := tc.state.Rank(); got != tc.rank {
			t.Fatalf("%s rank: got %d want %d", tc.state, got, tc.rank)
		}
	}
}

func TestFinalizeFileCheckIsPassed(t *testing.T) {
	r := &Report{Checked: CheckedFiles}
	r.Finalize(false, false, false)
	if r.Verdict != Passed || r.ExitCode != 0 {
		t.Fatalf("file check: got %s exit %d", r.Verdict, r.ExitCode)
	}
	if len(r.Skipped) != 0 {
		t.Fatalf("a file check must not invent a warning: %+v", r.Skipped)
	}
	if r.Checked != CheckedFiles {
		t.Fatalf("checked: %q", r.Checked)
	}
}

func TestFinalizeLiveThatDidNotStartIsNotChecked(t *testing.T) {
	r := &Report{Checked: CheckedFiles}
	r.Finalize(false, true, false)
	if r.Verdict != NotChecked || r.ExitCode != 2 {
		t.Fatalf("live that did not bring the chart up: got %s exit %d", r.Verdict, r.ExitCode)
	}
}

func TestFinalizeDependencyIsNotProven(t *testing.T) {
	r := &Report{
		Checked:  CheckedFiles,
		Findings: []Finding{{RuleID: "helm.dependency.not_updated", Severity: SeverityWarning}},
	}
	r.Finalize(true, false, false)
	if r.Verdict != Passed || r.Checked != CheckedFilesAndLive {
		t.Fatalf("packaged chart unchanged: got %s checked %s", r.Verdict, r.Checked)
	}
}

func TestFinalizeRequireRehearsal(t *testing.T) {
	r := &Report{}
	r.Finalize(false, false, true)
	if r.Verdict != NotChecked || r.ExitCode != 2 {
		t.Fatalf("require rehearsal: got %s exit %d", r.Verdict, r.ExitCode)
	}
	if len(r.Skipped) != 1 || r.Skipped[0].Code != "rehearsal_required" {
		t.Fatalf("missing reason: %+v", r.Skipped)
	}
}

func TestFinalizeProvenAndBlocked(t *testing.T) {
	proven := &Report{}
	proven.Finalize(true, false, true)
	if proven.Verdict != Proven || proven.Checked != CheckedFilesAndLive || proven.ExitCode != 0 {
		t.Fatalf("proven: %+v", proven)
	}

	blocked := &Report{Findings: []Finding{{Severity: SeverityBlocker}}}
	blocked.Finalize(true, false, false)
	if blocked.Verdict != Blocked || blocked.ExitCode != 1 {
		t.Fatalf("blocked: %s %d", blocked.Verdict, blocked.ExitCode)
	}

	missed := &Report{}
	missed.Finalize(false, true, false)
	if missed.Verdict != NotChecked || missed.ExitCode != 2 {
		t.Fatalf("could not check: %s %d", missed.Verdict, missed.ExitCode)
	}
}
