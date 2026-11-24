package check

import (
	"fmt"
	"strings"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

// APIs records removed APIs, deprecations, oversized CRDs, and immutable selectors.
func APIs(in Input, st *State) {
	next := in.NextKubernetes
	if st.NextAPIServerRan {
		// The next minor was dry-run on its own API server. The table's
		// end-of-life rows would repeat those warnings.
		next = ""
	}
	compatFindings := k8sschema.CheckCompatibility(st.HeadObjs, in.KubeVersion, next, in.ServerSideApply)
	if st.APIServerRan {
		compatFindings = dropRule(compatFindings, "k8s.api.removed")
	} else if !in.ManifestMode {
		compatFindings = downgradeGuardedRemovedAPIs(in.HeadDir, st.HeadObjs, compatFindings)
	}
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
	if st.APIServerRan {
		summary = fmt.Sprintf("current Kubernetes was checked by API server %s", st.APIServerVersion)
	}
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

func dropRule(in []verdict.Finding, id string) []verdict.Finding {
	out := in[:0]
	for _, f := range in {
		if f.RuleID == id {
			continue
		}
		out = append(out, f)
	}
	return out
}

// downgradeGuardedRemovedAPIs turns a k8s.api.removed blocker into a warning
// when the object's template tests .Capabilities.APIVersions for that removed
// API version. helm template includes legacy API versions in its built-in
// version set and --api-versions cannot remove them.
func downgradeGuardedRemovedAPIs(chartDir string, objs []k8sschema.ManifestObject, findings []verdict.Finding) []verdict.Finding {
	if chartDir == "" || len(objs) == 0 || len(findings) == 0 {
		return findings
	}
	byID := make(map[string][]k8sschema.ManifestObject, len(objs))
	for _, obj := range objs {
		if obj.Source == "" {
			continue
		}
		byID[obj.ID()] = append(byID[obj.ID()], obj)
	}
	for i := range findings {
		f := &findings[i]
		if f.RuleID != "k8s.api.removed" || f.Severity != verdict.SeverityBlocker {
			continue
		}
		for _, obj := range byID[f.Resource] {
			if !strings.Contains(f.Message, "`"+obj.APIVersion+"`") {
				continue
			}
			if helm.TemplateGuardsRemovedAPI(chartDir, obj.Source, obj.APIVersion, obj.Kind) {
				f.Severity = verdict.SeverityWarning
				note := "The template checks .Capabilities.APIVersions for `" + obj.APIVersion + "`. `helm template` includes legacy API versions in its default capability list and cannot remove them, so this removal is not proven."
				if f.Detail == "" {
					f.Detail = note
				} else {
					f.Detail = f.Detail + " " + note
				}
				break
			}
		}
	}
	return findings
}
