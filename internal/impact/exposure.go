package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

func exposure(before, after map[string]obj) []verdict.Finding {
	var findings []verdict.Finding
	for k, neu := range after {
		old, ok := before[k]
		switch neu.Kind {
		case "Service":
			if movedToExposed(serviceType(old, ok), serviceType(neu, true)) {
				findings = append(findings, verdict.Finding{
					RuleID:   "impact.exposure.service",
					Severity: verdict.SeverityWarning,
					Resource: neu.ID,
					Message:  fmt.Sprintf("%s type moves to %s.", neu.ID, serviceType(neu, true)),
				})
			}
		case "Ingress", "Gateway", "HTTPRoute", "GRPCRoute", "TLSRoute":
			added := newHosts(hosts(old, ok), hosts(neu, true))
			if len(added) > 0 {
				findings = append(findings, verdict.Finding{
					RuleID:   "impact.exposure.route",
					Severity: verdict.SeverityWarning,
					Resource: neu.ID,
					Message:  fmt.Sprintf("%s adds hosts %s.", neu.ID, strings.Join(added, ", ")),
				})
			}
		}
		if neu.Kind == "Deployment" || neu.Kind == "StatefulSet" || neu.Kind == "DaemonSet" || neu.Kind == "Pod" || neu.Kind == "Job" || neu.Kind == "CronJob" || neu.Kind == "ReplicaSet" {
			findings = append(findings, hostExposure(old, neu)...)
		}
	}
	for k, old := range before {
		if old.Kind != "NetworkPolicy" {
			continue
		}
		if _, ok := after[k]; !ok {
			findings = append(findings, verdict.Finding{
				RuleID:   "impact.exposure.networkpolicy",
				Severity: verdict.SeverityWarning,
				Resource: old.ID,
				Message:  fmt.Sprintf("%s is removed. Traffic it was restricting is no longer restricted by this policy.", old.ID),
			})
			continue
		}
		if opened(old, after[k]) {
			findings = append(findings, verdict.Finding{
				RuleID:   "impact.exposure.networkpolicy",
				Severity: verdict.SeverityWarning,
				Resource: old.ID,
				Message:  fmt.Sprintf("%s allows traffic it did not allow before.", old.ID),
			})
		}
	}
	return findings
}

func serviceType(o obj, ok bool) string {
	if !ok {
		return ""
	}
	t, _ := specOf(o.Body)["type"].(string)
	if t == "" {
		return "ClusterIP"
	}
	return t
}

func movedToExposed(before, after string) bool {
	if after != "LoadBalancer" && after != "NodePort" {
		return false
	}
	return before != after
}

func hosts(o obj, ok bool) []string {
	if !ok {
		return nil
	}
	var out []string
	spec := specOf(o.Body)
	switch o.Kind {
	case "Ingress":
		rules, _ := spec["rules"].([]any)
		for _, r := range rules {
			m, _ := r.(map[string]any)
			if h, _ := m["host"].(string); h != "" {
				out = append(out, h)
			}
		}
	case "Gateway":
		listeners, _ := spec["listeners"].([]any)
		for _, l := range listeners {
			m, _ := l.(map[string]any)
			if h, _ := m["hostname"].(string); h != "" {
				out = append(out, h)
			}
		}
	default:
		names, _ := spec["hostnames"].([]any)
		for _, n := range names {
			if s, ok := n.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func newHosts(before, after []string) []string {
	have := map[string]bool{}
	for _, h := range before {
		have[h] = true
	}
	var added []string
	seen := map[string]bool{}
	for _, h := range after {
		if have[h] || seen[h] {
			continue
		}
		seen[h] = true
		added = append(added, h)
	}
	sort.Strings(added)
	return added
}

func hostExposure(old, neu obj) []verdict.Finding {
	var findings []verdict.Finding
	oldSpec, _ := podSpec(old)
	newSpec, path := podSpec(neu)
	if newSpec == nil {
		return nil
	}
	if truthy(newSpec["hostNetwork"]) && (oldSpec == nil || !truthy(oldSpec["hostNetwork"])) {
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.exposure.host",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s sets hostNetwork.", neu.ID),
		})
	}
	if port := newHostPort(oldSpec, newSpec); port != "" {
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.exposure.host",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s adds a hostPort on %s.", neu.ID, path+"."+port),
		})
	}
	return findings
}

func newHostPort(oldSpec, newSpec map[string]any) string {
	oldPorts := hostPorts(oldSpec)
	for _, c := range containers(newSpec) {
		name, _ := c["name"].(string)
		ports, _ := c["ports"].([]any)
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			if _, ok := pm["hostPort"]; !ok {
				continue
			}
			pname, _ := pm["name"].(string)
			key := name + "/" + pname
			if pname == "" {
				key = name + "/" + fmt.Sprint(pm["containerPort"])
			}
			if _, ok := oldPorts[key]; !ok {
				return "containers[name=" + name + "].ports"
			}
		}
	}
	return ""
}

func hostPorts(spec map[string]any) map[string]bool {
	out := map[string]bool{}
	if spec == nil {
		return out
	}
	for _, c := range containers(spec) {
		name, _ := c["name"].(string)
		ports, _ := c["ports"].([]any)
		for _, p := range ports {
			pm, _ := p.(map[string]any)
			if _, ok := pm["hostPort"]; !ok {
				continue
			}
			pname, _ := pm["name"].(string)
			key := name + "/" + pname
			if pname == "" {
				key = name + "/" + fmt.Sprint(pm["containerPort"])
			}
			out[key] = true
		}
	}
	return out
}

func opened(old, neu obj) bool {
	return allowsAll(specOf(neu.Body)) && !allowsAll(specOf(old.Body))
}

func allowsAll(spec map[string]any) bool {
	for _, key := range []string{"ingress", "egress"} {
		rules, _ := spec[key].([]any)
		for _, r := range rules {
			m, _ := r.(map[string]any)
			if m == nil {
				continue
			}
			from, hasFrom := m["from"]
			to, hasTo := m["to"]
			if key == "ingress" && (!hasFrom || emptySlice(from)) {
				return true
			}
			if key == "egress" && (!hasTo || emptySlice(to)) {
				return true
			}
			peers, _ := from.([]any)
			if key == "egress" {
				peers, _ = to.([]any)
			}
			for _, p := range peers {
				pm, _ := p.(map[string]any)
				block, _ := pm["ipBlock"].(map[string]any)
				cidr, _ := block["cidr"].(string)
				if cidr == "0.0.0.0/0" || cidr == "::/0" {
					return true
				}
			}
		}
	}
	return false
}

func emptySlice(v any) bool {
	s, ok := v.([]any)
	return ok && len(s) == 0
}

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}
