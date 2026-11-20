package impact

import (
	"fmt"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
	"k8s.io/apimachinery/pkg/api/resource"
)

func capacity(before, after map[string]obj) []verdict.Finding {
	var findings []verdict.Finding
	for _, k := range orderedKeys(after) {
		neu := after[k]
		old, ok := before[k]
		if !ok {
			continue
		}
		switch neu.Kind {
		case "Deployment", "StatefulSet", "ReplicaSet":
			if down, hit := replicasDown(old, neu); hit {
				findings = append(findings, down)
			}
		case "PodDisruptionBudget":
			findings = append(findings, boundChanges(old, neu, "impact.capacity.pdb", "spec.minAvailable", "spec.maxUnavailable")...)
		case "HorizontalPodAutoscaler":
			findings = append(findings, boundChanges(old, neu, "impact.capacity.hpa", "spec.minReplicas", "spec.maxReplicas")...)
		}
		if workload(neu.Kind) {
			findings = append(findings, requestIncreases(old, neu)...)
		}
	}
	return findings
}

func replicasDown(old, neu obj) (verdict.Finding, bool) {
	b, bok := asInt(specOf(old.Body)["replicas"])
	a, aok := asInt(specOf(neu.Body)["replicas"])
	if !bok || !aok || a >= b {
		return verdict.Finding{}, false
	}
	return verdict.Finding{
		RuleID:   "impact.capacity.replicas",
		Severity: verdict.SeverityInfo,
		Resource: neu.ID,
		Message:  fmt.Sprintf("replicas decreased on %s.", neu.ID),
	}, true
}

func requestIncreases(old, neu obj) []verdict.Finding {
	oldSpec, _ := podSpec(old)
	newSpec, _ := podSpec(neu)
	oldC := containersByName(oldSpec)
	var findings []verdict.Finding
	for _, c := range containers(newSpec) {
		name, _ := c["name"].(string)
		var up []string
		if quantityUp(requests(oldC[name])["cpu"], requests(c)["cpu"]) {
			up = append(up, "cpu")
		}
		if quantityUp(requests(oldC[name])["memory"], requests(c)["memory"]) {
			up = append(up, "memory")
		}
		if len(up) == 0 {
			continue
		}
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.capacity.requests",
			Severity: verdict.SeverityInfo,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s request increased on container %s of %s.", strings.Join(up, " and "), name, neu.ID),
		})
	}
	return findings
}

func requests(c map[string]any) map[string]any {
	res, _ := c["resources"].(map[string]any)
	req, _ := res["requests"].(map[string]any)
	return req
}

func quantityUp(before, after any) bool {
	if before == nil || after == nil {
		return false
	}
	bq, err1 := resource.ParseQuantity(fmt.Sprint(before))
	aq, err2 := resource.ParseQuantity(fmt.Sprint(after))
	if err1 != nil || err2 != nil {
		return false
	}
	return aq.Cmp(bq) > 0
}

func boundChanges(old, neu obj, rule string, fields ...string) []verdict.Finding {
	var findings []verdict.Finding
	for _, field := range fields {
		if !fieldChanged(old.Body, neu.Body, field) {
			continue
		}
		findings = append(findings, verdict.Finding{
			RuleID:   rule,
			Severity: verdict.SeverityInfo,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s changed on %s.", field, neu.ID),
		})
	}
	return findings
}

func fieldChanged(old, neu map[string]any, path string) bool {
	return canonical(atPath(old, path)) != canonical(atPath(neu, path)) && (atPath(old, path) != nil || atPath(neu, path) != nil)
}

func atPath(body map[string]any, path string) any {
	cur := any(body)
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}
