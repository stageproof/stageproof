package check

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/knowledge"
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
		trial.APIVersions = knowledge.ServedAPIs(in.KubeVersion)
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

// Diff compares which values keys still change the rendered objects.
func Diff(ctx context.Context, in Input, st *State) {
	if len(in.HeadValues) == 0 && !in.ValuesFull {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "values.yaml AST",
			Status:  "pass",
			Summary: "no custom -f overlay passed (default chart values verified)",
		})
		return
	}
	base := in.renderOptions(in.BaseDir, baseValueFiles(in), in.BaseSet, in.BaseSetString, in.BaseSetFile, in.BaseRelease, in.BaseNamespace, in.BaseSkipCRDs)
	head := in.renderOptions(in.HeadDir, in.HeadValues, in.HeadSet, in.HeadSetString, in.HeadSetFile, in.HeadRelease, in.HeadNamespace, in.HeadSkipCRDs)
	// A parameters file already holds --set and helm.parameters. Leaving the
	// same assignments as --set puts a dropped key back, because --set wins over -f.
	dropMaterializedSets(&base)
	dropMaterializedSets(&head)
	base = st.applyRepair(base)
	head = st.applyRepair(head)
	if in.BaseDir != "" && !st.HeadUpgradeFallback {
		head.IsUpgrade = true
	}
	findings, sum, err := helm.ProveValues(ctx, helm.ProofRequest{
		BaseDir:     in.BaseDir,
		HeadDir:     in.HeadDir,
		ValuesFiles: in.HeadValues,
		Base:        base,
		Head:        head,
		Full:        in.ValuesFull,
	})
	if err != nil {
		st.Report.Findings = append(st.Report.Findings, verdict.Finding{
			RuleID:   "helm.values.parse_error",
			Severity: verdict.SeverityBlocker,
			Message:  err.Error(),
		})
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "values.yaml AST", Status: "fail", Summary: err.Error()})
		return
	}
	for i := range findings {
		if label, ok := in.ValueLabels[findings[i].File]; ok {
			findings[i].File = label
		}
	}
	st.Report.Findings = append(st.Report.Findings, findings...)
	summary := "no custom -f overlay passed (default chart values verified)"
	if sum.Keys > 0 {
		summary = sum.String()
	}
	status := "pass"
	if countBlockers(findings) > 0 {
		status = "fail"
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "values.yaml AST",
		Status:  status,
		Summary: summary,
	})
}

func baseValueFiles(in Input) []string {
	if len(in.BaseValues) == 0 {
		return in.HeadValues
	}
	out := append([]string{}, in.BaseValues...)
	have := map[string]bool{}
	for _, f := range out {
		have[f] = true
	}
	for _, f := range in.HeadValues {
		if !parameterFile(f) || have[f] {
			continue
		}
		out = append(out, f)
		have[f] = true
	}
	return out
}

func dropMaterializedSets(opts *helm.RenderOptions) {
	if opts == nil || !parameterized(opts.ValuesFiles) {
		return
	}
	opts.SetValues = nil
	opts.SetStrings = nil
}

func parameterized(files []string) bool {
	for _, f := range files {
		if parameterFile(f) {
			return true
		}
	}
	return false
}

func parameterFile(path string) bool {
	base := filepath.Base(path)
	return base == "parameters.yaml" || base == "helm-parameters.yaml"
}
