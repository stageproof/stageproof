package check

import (
	"fmt"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// APIs records removed APIs, deprecations, oversized CRDs, and immutable selectors.
func APIs(in Input, st *State) {
	compatFindings := k8sschema.CheckCompatibility(st.HeadObjs, in.KubeVersion, in.NextKubernetes, in.ServerSideApply)
	if len(st.BaseObjs) > 0 {
		compatFindings = append(compatFindings, k8sschema.DiffUpgradeSafety(st.BaseObjs, st.HeadObjs)...)
	}
	st.Report.Findings = append(st.Report.Findings, compatFindings...)
	k8sBlockers := countBlockers(compatFindings)
	if k8sBlockers > 0 {
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    fmt.Sprintf("k8s %s compatibility", in.KubeVersion),
			Status:  "fail",
			Summary: counted(k8sBlockers, "removed API or immutable upgrade blocker", "removed API or immutable upgrade blockers"),
		})
		return
	}
	summary := fmt.Sprintf("0 removed APIs or selector breaks on K8s %s", in.KubeVersion)
	if in.ServerSideApply {
		summary += " (SSA mode)"
	}
	if len(compatFindings) > 0 {
		summary = fmt.Sprintf("0 blockers (%s)", counted(len(compatFindings), "deprecation, EOL, or CRD warning", "deprecation, EOL, or CRD warnings"))
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    fmt.Sprintf("k8s %s compatibility", in.KubeVersion),
		Status:  "pass",
		Summary: summary,
	})
}
