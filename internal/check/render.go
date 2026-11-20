package check

import (
	"context"
	"fmt"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Charts renders the current chart, when there is one, and the new chart.
func Charts(ctx context.Context, in Input, st *State) {
	if in.BaseDir != "" {
		baseYAML, bErr := helm.RenderChart(ctx, helm.RenderOptions{
			ChartDir:    in.BaseDir,
			ValuesFiles: in.BaseValues,
			SetValues:   in.SetValues,
			SetStrings:  st.Placeholders,
			KubeVersion: in.KubeVersion,
		})
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

	headYAML, hErr := helm.RenderChart(ctx, helm.RenderOptions{
		ChartDir:    in.HeadDir,
		ValuesFiles: in.HeadValues,
		SetValues:   in.SetValues,
		SetStrings:  st.Placeholders,
		KubeVersion: in.KubeVersion,
	})
	if hErr != nil {
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
