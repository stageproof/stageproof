package check

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Run calls the stages in the order the check has always used.
// A required value that is new stops the check before the charts are rendered.
func Run(ctx context.Context, in Input) *verdict.Report {
	adapter := in.Adapter
	if adapter == "" {
		adapter = "helm"
		if in.ManifestMode {
			adapter = "manifests"
		}
	}
	checked := verdict.CheckedFiles
	if in.ManifestMode {
		checked = verdict.CheckedManifests
	}
	st := &State{
		Report: &verdict.Report{
			Adapter:     adapter,
			TargetK8s:   in.KubeVersion,
			NextK8sEOL:  in.NextKubernetes,
			BaseRef:     in.BaseRef,
			HeadRef:     in.HeadRef,
			ValuesFiles: labelPaths(in.HeadValues, in.ValueLabels),
			Checked:     checked,
			Timestamp:   in.Started.UTC(),
		},
		Started: in.Started,
		Require: in.RequireRehearsal,
	}
	if note := placeholderFinding(in.PlaceholderTokens); note != nil {
		st.Report.Findings = append(st.Report.Findings, *note)
	}
	if gap := valuesGapFinding(in.ValueGaps); gap != nil {
		st.Report.Findings = append(st.Report.Findings, *gap)
	}
	if in.ManifestMode && in.Live {
		st.Report.Skipped = append(st.Report.Skipped, verdict.SkipNotice{
			Code:        "manifest_live",
			Reason:      "--before and --after cannot be combined with a live rehearsal. Manifest mode does not install a chart.",
			Remediation: "Compare the rendered manifests without --live.",
		})
		st.stop(false, true)
		return st.Report
	}

	if in.ManifestMode {
		skipChartStages(st)
		base, bErr := loadRendered(in.ManifestBefore)
		if bErr != nil {
			return manifestFail(st, in.ManifestBefore, bErr)
		}
		head, hErr := loadRendered(in.ManifestAfter)
		if hErr != nil {
			return manifestFail(st, in.ManifestAfter, hErr)
		}
		st.BaseObjs = base
		st.HeadObjs = head
	} else {
		Gate(ctx, in, st)
		if st.Stopped {
			return st.Report
		}
		Charts(ctx, in, st)
		if st.Stopped {
			return st.Report
		}
		Diff(ctx, in, st)
		// Signals are recorded before compatibility findings, matching the previous order.
		Changes(in, st)
	}
	ServerProof(ctx, in, st)
	if st.Stopped {
		return st.Report
	}
	Impact(in, st)
	APIs(in, st)

	runtimeOK, inconclusive := Rehearse(ctx, in, st)
	// --live was asked for. A file check that passed while the chart never
	// came up is not PASSED, unless the report already names why the rehearsal
	// stopped. Those reasons stay PASSED, and --require-rehearsal turns them
	// into NOT CHECKED. PASSED also stays for a run that did not attempt --live.
	if in.Live && !runtimeOK && len(st.Report.Skipped) == 0 {
		inconclusive = true
	}
	st.Report.Findings = report.OrderFindings(st.Report.Findings)
	st.Report.DurationMs = time.Since(in.Started).Milliseconds()
	st.Report.Finalize(runtimeOK, inconclusive, in.RequireRehearsal)
	return st.Report
}

func skipChartStages(st *State) {
	skipped := []verdict.CheckResult{
		{Name: "required values gate", Status: "skip", Summary: "Skipped. These inputs are rendered manifests, not Helm charts."},
		{Name: "helm template", Status: "skip", Summary: "Skipped. The manifests were already rendered."},
		{Name: "values.yaml AST", Status: "skip", Summary: "Skipped. There is no chart template to read."},
		{Name: "upgrade signals", Status: "skip", Summary: "Skipped. There is no chart to compare."},
	}
	st.Report.Checks = append(st.Report.Checks, skipped...)
}

func manifestFail(st *State, path string, err error) *verdict.Report {
	st.Report.Findings = append(st.Report.Findings, verdict.Finding{
		RuleID:   "manifest.invalid_yaml",
		Severity: verdict.SeverityBlocker,
		File:     path,
		Message:  "The rendered manifests could not be read.",
		Detail:   err.Error(),
	})
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "rendered manifests",
		Status:  "fail",
		Summary: err.Error(),
	})
	st.stop(false, false)
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

func valuesGapFinding(refs []string) *verdict.Finding {
	seen := map[string]bool{}
	var sorted []string
	for _, ref := range refs {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		sorted = append(sorted, ref)
	}
	if len(sorted) == 0 {
		return nil
	}
	sort.Strings(sorted)
	return &verdict.Finding{
		RuleID:   "flux.values.unreadable",
		Severity: verdict.SeverityWarning,
		Message:  "Helm values are incomplete. " + strings.Join(sorted, " "),
		Detail:   "A valuesFrom ConfigMap or Secret was not read. The chart was still rendered. The missing reference was not filled in, and a rehearsal does not make this result PROVEN.",
	}
}
