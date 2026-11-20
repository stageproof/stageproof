package check

import (
	"context"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/rehearse"
	"github.com/stageproof/stageproof/internal/verdict"
)

// Rehearse installs, upgrades, and rolls back when --live was asked for and
// nothing is already blocked.
func Rehearse(ctx context.Context, in Input, st *State) (runtimeOK, inconclusive bool) {
	if !in.Live || countBlockers(st.Report.Findings) > 0 {
		return false, false
	}
	extCRDs := k8sschema.DetectExternalCRDs(append(st.BaseObjs, st.HeadObjs...))
	outcome, rErr := rehearse.RunKindRehearsal(ctx, rehearse.KindOptions{
		KubernetesVersion: in.KubeVersion,
		ClusterName:       in.ClusterName,
		KeepCluster:       in.KeepCluster,
		BaseChartDir:      in.BaseDir,
		HeadChartDir:      in.HeadDir,
		BaseValues:        in.BaseValues,
		HeadValues:        in.HeadValues,
		SetValues:         in.SetValues,
		SetStrings:        st.Placeholders,
		ExternalCRDs:      extCRDs,
		ExtraCRDFiles:     in.CRDFiles,
		Timeout:           in.Timeout,
		RunHelmTest:       in.HelmTest,
	})
	if rErr != nil {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{Name: "kind live test", Status: "fail", Summary: rErr.Error()})
		return false, true
	}
	if outcome.Rehearsed {
		st.Report.Checked = verdict.CheckedFilesAndLive
	}
	st.Report.Checks = append(st.Report.Checks, outcome.Checks...)
	st.Report.Findings = append(st.Report.Findings, outcome.Findings...)
	st.Report.Skipped = append(st.Report.Skipped, outcome.Degraded...)
	st.Report.ExcludedChecks = append(st.Report.ExcludedChecks, outcome.ExcludedChecks...)
	if outcome.ClusterName != "" {
		st.Report.Cluster = outcome.ClusterName
	}
	return outcome.RuntimeSucceeded, outcome.Inconclusive
}
