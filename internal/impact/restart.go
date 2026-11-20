package impact

import (
	"fmt"

	"github.com/stageproof/stageproof/internal/verdict"
)

func restarts(before, after map[string]obj) []verdict.Finding {
	var findings []verdict.Finding
	for k, old := range before {
		neu, ok := after[k]
		if !ok || !workload(old.Kind) {
			continue
		}
		oldT, path := podTemplate(old)
		newT, _ := podTemplate(neu)
		diff := firstDiff(oldT, newT, path)
		if diff == "" {
			continue
		}
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.restart",
			Severity: verdict.SeverityInfo,
			Resource: old.ID,
			Message:  fmt.Sprintf("The pod template of %s changed at %s. The pods will restart. A restart is normal for an upgrade.", old.ID, diff),
		})
	}
	return findings
}

func workload(kind string) bool {
	switch kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod":
		return true
	default:
		return false
	}
}

// podTemplate returns the pod template as any. A missing template is a nil map,
// which is not a nil interface, so callers compare the two templates with firstDiff.
func podTemplate(o obj) (any, string) {
	spec := specOf(o.Body)
	if o.Kind == "Pod" {
		return spec, "spec"
	}
	if o.Kind == "CronJob" {
		job, _ := spec["jobTemplate"].(map[string]any)
		js, _ := job["spec"].(map[string]any)
		t, _ := js["template"].(map[string]any)
		return t, "spec.jobTemplate.spec.template"
	}
	t, _ := spec["template"].(map[string]any)
	return t, "spec.template"
}
