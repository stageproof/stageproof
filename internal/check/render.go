package check

import (
	"context"
	"fmt"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

func (in Input) renderOptions(dir string, values, sets, setStrings, setFiles []string, release, namespace string, skip bool) helm.RenderOptions {
	return helm.RenderOptions{
		ReleaseName: release,
		Namespace:   namespace,
		ChartDir:    dir,
		ValuesFiles: values,
		SetValues:   append(append([]string{}, sets...), in.SetValues...),
		SetStrings:  append([]string{}, setStrings...),
		SetFiles:    setFiles,
		KubeVersion: in.KubeVersion,
		SkipCRDs:    skip,
	}
}

// Charts renders the current chart, when there is one, and the new chart.
func Charts(ctx context.Context, in Input, st *State) {
	NoteChartAPI(in, st)
	if in.BaseDir != "" {
		baseOpts := in.renderOptions(in.BaseDir, in.BaseValues, in.BaseSet, in.BaseSetString, in.BaseSetFile, in.BaseRelease, in.BaseNamespace, in.BaseSkipCRDs)
		baseOpts.SetStrings = append(baseOpts.SetStrings, st.Placeholders...)
		baseYAML, bErr := helm.RenderChart(ctx, baseOpts)
		if bErr != nil {
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
				Name:    "baseline helm template",
				Status:  "fail",
				Summary: bErr.Error(),
			})
			st.stop(false, true)
			return
		}
		objs, pErr := k8sschema.ParseManifestStream(baseYAML)
		if pErr != nil {
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "baseline manifest parse", Status: "fail", Summary: pErr.Error()})
			st.stop(false, true)
			return
		}
		st.BaseObjs = objs
	}

	headOpts := in.renderOptions(in.HeadDir, in.HeadValues, in.HeadSet, in.HeadSetString, in.HeadSetFile, in.HeadRelease, in.HeadNamespace, in.HeadSkipCRDs)
	headOpts.SetStrings = append(headOpts.SetStrings, st.Placeholders...)
	headYAML, hErr := helm.RenderChart(ctx, headOpts)
	if hErr != nil {
		if detail, ok := helm.RequiredFailure(hErr, helm.SecretStrings(in.HeadValues)); ok {
			st.Report.Findings = append(st.Report.Findings, verdict.Finding{
				RuleID:   "helm.values.new_required_unset",
				Severity: verdict.SeverityBlocker,
				File:     in.HeadDir,
				Message:  "The new chart does not render because a required value is unset.",
				Detail:   detail,
				Proof:    "render",
			})
			st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
				Name:    "helm template",
				Status:  "fail",
				Summary: "a required value is unset",
			})
			st.stop(false, false)
			return
		}
		st.Report.Findings = append(st.Report.Findings, verdict.Finding{
			RuleID:   "helm.template.render_failed",
			Severity: verdict.SeverityBlocker,
			File:     in.HeadDir,
			Message:  "Candidate chart (`--to`) failed `helm template` rendering.",
			Detail:   hErr.Error(),
		})
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "helm template",
			Status:  "fail",
			Summary: "render failed",
		})
		st.stop(false, false)
		return
	}
	headObjs, hpErr := k8sschema.ParseManifestStream(headYAML)
	if hpErr != nil {
		st.Report.Findings = append(st.Report.Findings, verdict.Finding{
			RuleID:   "helm.template.invalid_yaml",
			Severity: verdict.SeverityBlocker,
			File:     in.HeadDir,
			Message:  hpErr.Error(),
		})
		st.stop(false, false)
		return
	}
	st.HeadObjs = headObjs
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "helm template",
		Status:  "pass",
		Summary: fmt.Sprintf("rendered %s cleanly", counted(len(headObjs), "Kubernetes object", "Kubernetes objects")),
	})
}
