package check

import (
	"context"
	"fmt"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Gate stops the check when the new chart requires a value the current chart
// did not require. Otherwise it records the placeholders render may need.
func Gate(ctx context.Context, in Input, st *State) {
	sets := append(append([]string{}, in.SetValues...), in.HeadSet...)
	placeholders, blockers := helm.PlanRequiredPlaceholders(in.BaseDir, in.HeadDir, in.HeadValues, sets)
	if len(blockers) > 0 {
		st.Report.Findings = append(st.Report.Findings, blockers...)
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "required values gate",
			Status:  "fail",
			Summary: fmt.Sprintf("%s introduced by the upgrade", counted(len(blockers), "new unset `required` value", "new unset `required` values")),
		})
		st.stop(false, false)
		return
	}
	if len(placeholders) > 0 {
		// A template can mention required on a path the current values never
		// reach. If the chart renders without a placeholder, the key is not a
		// secret the chart refuses to start without.
		trial := in.renderOptions(in.HeadDir, in.HeadValues, in.HeadSet, in.HeadSetString, in.HeadSetFile, in.HeadRelease, in.HeadNamespace, in.HeadSkipCRDs)
		if _, trialErr := helm.RenderChart(ctx, trial); trialErr == nil {
			placeholders = nil
		}
	}
	if len(placeholders) > 0 {
		var keysOnly []string
		for _, kv := range placeholders {
			keysOnly = append(keysOnly, strings.SplitN(kv, "=", 2)[0])
		}
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "required values shim",
			Status:  "warn",
			Summary: fmt.Sprintf("the chart does not render unless %s is set: %s", counted(len(keysOnly), "this value", "these values"), strings.Join(keysOnly, ", ")),
		})
	}
	st.Placeholders = placeholders
}

// Diff compares which values keys the current chart reads with the new chart.
func Diff(in Input, st *State) {
	if len(in.HeadValues) > 0 {
		valFindings, _, _, vErr := helm.DiffValuesConsumption(in.BaseDir, in.HeadDir, in.HeadValues)
		if vErr != nil {
			st.Report.Findings = append(st.Report.Findings, verdict.Finding{
				RuleID:   "helm.values.parse_error",
				Severity: verdict.SeverityBlocker,
				Message:  vErr.Error(),
			})
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "values.yaml AST", Status: "fail", Summary: vErr.Error()})
			return
		}
		for i := range valFindings {
			if label, ok := in.ValueLabels[valFindings[i].File]; ok {
				valFindings[i].File = label
			}
		}
		st.Report.Findings = append(st.Report.Findings, valFindings...)
		dropped := countBlockers(valFindings)
		if dropped > 0 {
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
				Name:    "values.yaml AST",
				Status:  "fail",
				Summary: fmt.Sprintf("%s across %s", counted(dropped, "silently dropped key", "silently dropped keys"), counted(len(in.HeadValues), "overlay", "overlays")),
			})
			return
		}
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "values.yaml AST",
			Status:  "pass",
			Summary: fmt.Sprintf("every user key in %s is still read", counted(len(in.HeadValues), "overlay", "overlays")),
		})
		return
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "values.yaml AST",
		Status:  "pass",
		Summary: "no custom -f overlay passed (default chart values verified)",
	})
}
