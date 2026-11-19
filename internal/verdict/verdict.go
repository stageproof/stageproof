package verdict

import "time"

// State is the result of one check. The name is what a person reads.
// The exit code is what CI reads.
//
//	PROVEN       exit 0   file checks passed and the upgrade was rehearsed
//	PASSED       exit 0   file checks passed; the upgrade was not rehearsed
//	BLOCKED      exit 1   the upgrade has a problem the report names
//	NOT CHECKED  exit 2   the check did not finish, or a required rehearsal did not run
type State string

const (
	Proven     State = "PROVEN"
	Passed     State = "PASSED"
	Blocked    State = "BLOCKED"
	NotChecked State = "NOT CHECKED"
)

// What a check actually exercised. Printed as "checked: files"
// or "checked: files, live cluster".
const (
	CheckedFiles        = "files"
	CheckedFilesAndLive = "files, live cluster"
)

func (s State) ExitCode() int {
	switch s {
	case Proven, Passed:
		return 0
	case Blocked:
		return 1
	default:
		return 2
	}
}

// Rank orders results when one pull request contains several checks.
// Higher is worse. PASSED outranks PROVEN because less was exercised,
// even though both exit 0.
func (s State) Rank() int {
	switch s {
	case Blocked:
		return 3
	case NotChecked:
		return 2
	case Passed:
		return 1
	default:
		return 0
	}
}

type Severity string

const (
	SeverityBlocker Severity = "blocker"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type CheckResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // pass, fail, warn, skip
	Summary string `json:"summary"`
}

type Finding struct {
	RuleID   string   `json:"rule_id"`
	Severity Severity `json:"severity"`
	Resource string   `json:"resource,omitempty"`
	File     string   `json:"file,omitempty"`
	Message  string   `json:"message"`
	Detail   string   `json:"detail,omitempty"`
}

// SkipNotice explains why a live rehearsal did not finish.
// It is not a failure of the chart. The result stays PASSED unless
// --require-rehearsal was set, in which case the result is NOT CHECKED.
type SkipNotice struct {
	Code        string `json:"code"`
	Reason      string `json:"reason"`
	Remediation string `json:"remediation"`
}

// DegradedNotice is the older name of a skip notice.
type DegradedNotice = SkipNotice

type Report struct {
	Verdict        State         `json:"verdict"`
	ExitCode       int           `json:"exit_code"`
	Adapter        string        `json:"adapter"`
	TargetK8s      string        `json:"target_k8s"`
	NextK8sEOL     string        `json:"next_k8s_eol,omitempty"`
	BaseRef        string        `json:"base_ref"`
	HeadRef        string        `json:"head_ref"`
	ValuesFiles    []string      `json:"values_files,omitempty"`
	Checked        string        `json:"checked"`
	DurationMs     int64         `json:"duration_ms"`
	Timestamp      time.Time     `json:"timestamp"`
	Checks         []CheckResult `json:"checks"`
	Findings       []Finding     `json:"findings,omitempty"`
	Skipped        []SkipNotice  `json:"skipped,omitempty"`
	ExcludedChecks []string      `json:"excluded_checks,omitempty"`
}

// Finalize sets the result from what the check actually did.
//
//  1. A blocker finding is BLOCKED.
//  2. A check that could not finish is NOT CHECKED.
//  3. File checks plus a completed rehearsal are PROVEN.
//  4. File checks without a rehearsal are PASSED, unless requireRehearsal
//     is set, which makes that NOT CHECKED.
func (r *Report) hasRule(id string) bool {
	for _, f := range r.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func (r *Report) Finalize(runtimeSucceeded, couldNotCheck, requireRehearsal bool) {
	if r.Checked == "" {
		r.Checked = CheckedFiles
	}

	hasBlocker := false
	for _, f := range r.Findings {
		if f.Severity == SeverityBlocker {
			hasBlocker = true
			break
		}
	}

	switch {
	case hasBlocker:
		r.Verdict = Blocked
	case couldNotCheck:
		r.Verdict = NotChecked
	case runtimeSucceeded && len(r.Skipped) == 0:
		r.Checked = CheckedFilesAndLive
		// A green rehearsal of the chart on disk is not proof of a dependency
		// constraint that was never packaged under charts/.
		if r.hasRule("helm.dependency.not_updated") {
			r.Verdict = Passed
		} else {
			r.Verdict = Proven
		}
	case requireRehearsal:
		r.Verdict = NotChecked
		if len(r.Skipped) == 0 {
			r.Skipped = append(r.Skipped, DegradedNotice{
				Code:        "rehearsal_required",
				Reason:      "File checks passed. --require-rehearsal is set and the upgrade was not rehearsed in a cluster.",
				Remediation: "Re-run with --live. That starts a temporary kind cluster on this machine, installs the current chart, upgrades to the new chart, and rolls back. It does not commit and it does not change any cluster you already have.",
			})
			r.ExcludedChecks = appendUnique(r.ExcludedChecks, "kind.upgrade_rollback")
		}
	default:
		r.Verdict = Passed
	}

	r.ExitCode = r.Verdict.ExitCode()
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
}

func appendUnique(list []string, item string) []string {
	for _, x := range list {
		if x == item {
			return list
		}
	}
	return append(list, item)
}
