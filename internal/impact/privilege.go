package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

func privilege(before, after map[string]obj) []verdict.Finding {
	var findings []verdict.Finding
	for k, neu := range after {
		old, ok := before[k]
		switch neu.Kind {
		case "ClusterRole", "Role":
			if f, hit := rbacFinding(old, ok, neu); hit {
				findings = append(findings, f)
			}
		case "ClusterRoleBinding", "RoleBinding":
			if f, hit := bindingFinding(old, ok, neu); hit {
				findings = append(findings, f)
			}
		case "ValidatingWebhookConfiguration", "MutatingWebhookConfiguration":
			findings = append(findings, webhookFindings(old, ok, neu)...)
		case "CustomResourceDefinition":
			if strategy(neu) == "Webhook" && strategy(old) != "Webhook" {
				findings = append(findings, verdict.Finding{
					RuleID:   "impact.privilege.conversion",
					Severity: verdict.SeverityWarning,
					Resource: neu.ID,
					Message:  fmt.Sprintf("%s sets conversion.strategy to Webhook.", neu.ID),
				})
			}
		}
		if workload(neu.Kind) {
			findings = append(findings, securityFindings(old, neu)...)
		}
	}
	return findings
}

func rbacFinding(old obj, hasOld bool, neu obj) (verdict.Finding, bool) {
	oldRules := map[string]map[string]any{}
	if hasOld {
		oldRules = ruleSet(rulesOf(old.Body))
	}
	var flags []string
	added := 0
	for sig, rule := range ruleSet(rulesOf(neu.Body)) {
		if _, ok := oldRules[sig]; ok {
			continue
		}
		added++
		flags = append(flags, ruleFlags(rule, neu.Kind == "ClusterRole")...)
	}
	if added == 0 {
		return verdict.Finding{}, false
	}
	sort.Strings(flags)
	flags = unique(flags)
	msg := fmt.Sprintf("%s adds rules that the current chart did not have.", neu.ID)
	if len(flags) > 0 {
		msg = fmt.Sprintf("%s adds rules. Flags: %s.", neu.ID, strings.Join(flags, ", "))
	}
	return verdict.Finding{
		RuleID:   "impact.privilege.rbac",
		Severity: verdict.SeverityWarning,
		Resource: neu.ID,
		Message:  msg,
	}, true
}

func rulesOf(body map[string]any) []any {
	r, _ := body["rules"].([]any)
	return r
}

func ruleSet(rules []any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range rules {
		m, ok := asMap(r)
		if !ok {
			continue
		}
		norm := normalizeRule(m)
		out[canonical(norm)] = norm
	}
	return out
}

func normalizeRule(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"apiGroups", "resources", "verbs", "resourceNames", "nonResourceURLs"} {
		if v, ok := m[k]; ok {
			out[k] = sortedStrings(v)
		}
	}
	return out
}

func sortedStrings(v any) []string {
	s, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range s {
		out = append(out, fmt.Sprint(item))
	}
	sort.Strings(out)
	return out
}

func ruleFlags(rule map[string]any, cluster bool) []string {
	var out []string
	if listHas(rule["apiGroups"], "*") || listHas(rule["resources"], "*") || listHas(rule["verbs"], "*") {
		out = append(out, "wildcard")
	}
	if cluster && (listHas(rule["resources"], "secrets") || listHas(rule["resources"], "*")) && verbReads(rule["verbs"]) {
		out = append(out, "cluster-wide secrets read")
	}
	for _, v := range []string{"escalate", "bind", "impersonate"} {
		if listHas(rule["verbs"], v) {
			out = append(out, v)
		}
	}
	if listHas(rule["resources"], "pods/exec") {
		out = append(out, "pods/exec")
	}
	return out
}

func listHas(v any, want string) bool {
	switch s := v.(type) {
	case []string:
		for _, item := range s {
			if item == want {
				return true
			}
		}
	case []any:
		for _, item := range s {
			if fmt.Sprint(item) == want {
				return true
			}
		}
	}
	return false
}

func verbReads(v any) bool {
	for _, want := range []string{"get", "list", "watch", "*"} {
		if listHas(v, want) {
			return true
		}
	}
	return false
}

func bindingFinding(old obj, hasOld bool, neu obj) (verdict.Finding, bool) {
	if !bindsClusterAdmin(neu.Body) {
		return verdict.Finding{}, false
	}
	if !hasOld || !bindsClusterAdmin(old.Body) || subjectsGrew(old.Body, neu.Body) {
		return verdict.Finding{
			RuleID:   "impact.privilege.binding",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s binds to cluster-admin.", neu.ID),
		}, true
	}
	return verdict.Finding{}, false
}

func bindsClusterAdmin(body map[string]any) bool {
	ref, _ := body["roleRef"].(map[string]any)
	name, _ := ref["name"].(string)
	kind, _ := ref["kind"].(string)
	return name == "cluster-admin" && (kind == "" || kind == "ClusterRole")
}

func subjectsGrew(old, neu map[string]any) bool {
	have := map[string]bool{}
	for _, s := range subjectKeys(old) {
		have[s] = true
	}
	for _, s := range subjectKeys(neu) {
		if !have[s] {
			return true
		}
	}
	return false
}

func subjectKeys(body map[string]any) []string {
	subs, _ := body["subjects"].([]any)
	var out []string
	for _, s := range subs {
		out = append(out, canonical(s))
	}
	return out
}

func strategy(o obj) string {
	if o.Body == nil {
		return ""
	}
	conv, _ := specOf(o.Body)["conversion"].(map[string]any)
	s, _ := conv["strategy"].(string)
	return s
}

func webhookFindings(old obj, hasOld bool, neu obj) []verdict.Finding {
	oldNames := map[string]bool{}
	if hasOld {
		for _, w := range webhooks(old.Body) {
			if dangerousWebhook(w) {
				oldNames[webhookName(w)] = true
			}
		}
	}
	var findings []verdict.Finding
	for _, w := range webhooks(neu.Body) {
		name := webhookName(w)
		if !dangerousWebhook(w) || oldNames[name] {
			continue
		}
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.privilege.webhook",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s webhook %s has failurePolicy Fail and no namespace selector.", neu.ID, name),
		})
	}
	return findings
}

func webhooks(body map[string]any) []map[string]any {
	raw, _ := body["webhooks"].([]any)
	var out []map[string]any
	for _, w := range raw {
		if m, ok := asMap(w); ok {
			out = append(out, m)
		}
	}
	return out
}

func webhookName(w map[string]any) string {
	s, _ := w["name"].(string)
	return s
}

func dangerousWebhook(w map[string]any) bool {
	policy, _ := w["failurePolicy"].(string)
	if policy != "" && policy != "Fail" {
		return false
	}
	_, hasNS := w["namespaceSelector"]
	return !hasNS
}

func securityFindings(old, neu obj) []verdict.Finding {
	var findings []verdict.Finding
	oldSpec, _ := podSpec(old)
	newSpec, _ := podSpec(neu)
	if newSpec == nil {
		return nil
	}
	if fields := weakerContext(ctxOf(oldSpec), ctxOf(newSpec)); len(fields) > 0 {
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.privilege.securitycontext",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s pod securityContext is weaker at %s.", neu.ID, strings.Join(fields, ", ")),
		})
	}
	oldC := containersByName(oldSpec)
	for _, c := range containers(newSpec) {
		name, _ := c["name"].(string)
		fields := weakerContext(ctxOf(oldC[name]), ctxOf(c))
		if len(fields) == 0 {
			continue
		}
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.privilege.securitycontext",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s container %s securityContext is weaker at %s.", neu.ID, name, strings.Join(fields, ", ")),
		})
	}
	if vols := newHostPaths(oldSpec, newSpec); len(vols) > 0 {
		findings = append(findings, verdict.Finding{
			RuleID:   "impact.privilege.hostpath",
			Severity: verdict.SeverityWarning,
			Resource: neu.ID,
			Message:  fmt.Sprintf("%s adds hostPath volumes %s.", neu.ID, strings.Join(vols, ", ")),
		})
	}
	return findings
}

func ctxOf(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	c, _ := m["securityContext"].(map[string]any)
	return c
}

func weakerContext(old, neu map[string]any) []string {
	if neu == nil {
		return nil
	}
	var fields []string
	if truthy(neu["privileged"]) && !truthy(old["privileged"]) {
		fields = append(fields, "privileged")
	}
	if old != nil && old["allowPrivilegeEscalation"] == false && neu["allowPrivilegeEscalation"] != false {
		fields = append(fields, "allowPrivilegeEscalation")
	}
	if truthy(old["runAsNonRoot"]) && !truthy(neu["runAsNonRoot"]) {
		fields = append(fields, "runAsNonRoot")
	}
	if truthy(old["readOnlyRootFilesystem"]) && !truthy(neu["readOnlyRootFilesystem"]) {
		fields = append(fields, "readOnlyRootFilesystem")
	}
	if u, ok := asInt(neu["runAsUser"]); ok && u == 0 {
		if prev, pok := asInt(old["runAsUser"]); !pok || prev != 0 {
			fields = append(fields, "runAsUser")
		}
	}
	if caps := weakerCaps(old, neu); len(caps) > 0 {
		fields = append(fields, caps...)
	}
	return fields
}

func weakerCaps(old, neu map[string]any) []string {
	oc, _ := old["capabilities"].(map[string]any)
	nc, _ := neu["capabilities"].(map[string]any)
	var out []string
	if added := stringDiff(capList(nc, "add"), capList(oc, "add")); len(added) > 0 {
		out = append(out, "capabilities.add")
	}
	if removed := stringDiff(capList(oc, "drop"), capList(nc, "drop")); len(removed) > 0 {
		out = append(out, "capabilities.drop")
	}
	return out
}

func capList(c map[string]any, key string) []string {
	if c == nil {
		return nil
	}
	return sortedStrings(c[key])
}

func stringDiff(after, before []string) []string {
	have := map[string]bool{}
	for _, s := range before {
		have[s] = true
	}
	var out []string
	for _, s := range after {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

func containers(spec map[string]any) []map[string]any {
	if spec == nil {
		return nil
	}
	var out []map[string]any
	for _, key := range []string{"containers", "initContainers", "ephemeralContainers"} {
		raw, _ := spec[key].([]any)
		for _, c := range raw {
			if m, ok := asMap(c); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func containersByName(spec map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, c := range containers(spec) {
		name, _ := c["name"].(string)
		out[name] = c
	}
	return out
}

func podSpec(o obj) (map[string]any, string) {
	if o.Body == nil {
		return nil, ""
	}
	spec := specOf(o.Body)
	if o.Kind == "Pod" {
		return spec, "spec"
	}
	if o.Kind == "CronJob" {
		job, _ := spec["jobTemplate"].(map[string]any)
		js, _ := job["spec"].(map[string]any)
		t, _ := js["template"].(map[string]any)
		ps, _ := t["spec"].(map[string]any)
		return ps, "spec.jobTemplate.spec.template.spec"
	}
	t, _ := spec["template"].(map[string]any)
	ps, _ := t["spec"].(map[string]any)
	return ps, "spec.template.spec"
}

func newHostPaths(oldSpec, newSpec map[string]any) []string {
	have := hostPathNames(oldSpec)
	var names []string
	seen := map[string]bool{}
	for _, v := range volumes(newSpec) {
		name, _ := v["name"].(string)
		if _, ok := v["hostPath"]; !ok || have[name] || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func hostPathNames(spec map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, v := range volumes(spec) {
		if _, ok := v["hostPath"]; !ok {
			continue
		}
		name, _ := v["name"].(string)
		out[name] = true
	}
	return out
}

func volumes(spec map[string]any) []map[string]any {
	if spec == nil {
		return nil
	}
	raw, _ := spec["volumes"].([]any)
	var out []map[string]any
	for _, v := range raw {
		if m, ok := asMap(v); ok {
			out = append(out, m)
		}
	}
	return out
}

func unique(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
