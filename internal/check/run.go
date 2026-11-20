package check

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

// Run calls the stages in the order the check has always used.
// A required value that is new stops the check before the charts are rendered.
func Run(ctx context.Context, in Input) *verdict.Report {
	st := &State{
		Report: &verdict.Report{
			Adapter:     "helm",
			TargetK8s:   in.KubeVersion,
			NextK8sEOL:  in.NextKubernetes,
			BaseRef:     in.BaseRef,
			HeadRef:     in.HeadRef,
			ValuesFiles: labelPaths(in.HeadValues, in.ValueLabels),
			Checked:     verdict.CheckedFiles,
			Timestamp:   in.Started.UTC(),
		},
		Started: in.Started,
		Require: in.RequireRehearsal,
	}
	if note := placeholderFinding(in.PlaceholderTokens); note != nil {
		st.Report.Findings = append(st.Report.Findings, *note)
	}

	Gate(ctx, in, st)
	if st.Stopped {
		return st.Report
	}
	Charts(ctx, in, st)
	if st.Stopped {
		return st.Report
	}
	Diff(in, st)
	// Signals are recorded before compatibility findings, matching the previous order.
	Changes(in, st)
	ServerProof(ctx, in, st)
	if st.Stopped {
		return st.Report
	}
	APIs(in, st)

	runtimeOK, inconclusive := Rehearse(ctx, in, st)
	// --live was asked for. A file check that passed while the chart never
	// came up is not PASSED, unless the report already names why the rehearsal
	// stopped. Those reasons stay PASSED, and --require-rehearsal turns them
	// into NOT CHECKED. PASSED also stays for a run that did not attempt --live.
	if in.Live && !runtimeOK && len(st.Report.Skipped) == 0 {
		inconclusive = true
	}
	st.Report.DurationMs = time.Since(in.Started).Milliseconds()
	st.Report.Finalize(runtimeOK, inconclusive, in.RequireRehearsal)
	return st.Report
}

func placeholderFinding(tokens []string) *verdict.Finding {
	seen := map[string]bool{}
	var uniq []string
	for _, token := range tokens {
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		uniq = append(uniq, token)
	}
	if len(uniq) == 0 {
		return nil
	}
	sort.Strings(uniq)
	return &verdict.Finding{
		RuleID:   "helm.config.placeholder_used",
		Severity: verdict.SeverityInfo,
		Message:  "A placeholder was used for " + strings.Join(uniq, ", ") + ".",
		Detail:   "An unresolved ${...} token outside the chart repository, name, and version was replaced with stageproof-placeholder.",
	}
}
