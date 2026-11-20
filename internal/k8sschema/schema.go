package k8sschema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

type ManifestObject struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   ObjectMetadata `yaml:"metadata"`
	Spec       map[string]any `yaml:"spec"`
	// Object is the whole document. ConfigMap data, RBAC rules, CRD schemas,
	// and webhook config live outside spec.
	Object   map[string]any `yaml:"-"`
	RawBytes int            `yaml:"-"`
	RawYAML  string         `yaml:"-"`
}

type ObjectMetadata struct {
	Name        string            `yaml:"name"`
	Namespace   string            `yaml:"namespace"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

func (m ManifestObject) ID() string {
	ns := m.Metadata.Namespace
	if ns == "" {
		ns = "default"
	}
	// The printed form stays Kind/namespace/name. A core object has no group
	// to print. Other groups are part of matchKey, not this string, so a
	// report does not change.
	return fmt.Sprintf("%s/%s/%s", m.Kind, ns, m.Metadata.Name)
}

// Group is the API group. A core object (apiVersion v1) has an empty group.
func (m ManifestObject) Group() string {
	if !strings.Contains(m.APIVersion, "/") {
		return ""
	}
	group, _, _ := strings.Cut(m.APIVersion, "/")
	return group
}

// matchKey separates two objects that share a kind and a name in different
// API groups. The printed ID stays the same.
func (m ManifestObject) matchKey() string {
	return m.Group() + "\x00" + m.ID()
}

type apiRemovalRule struct {
	APIVersion   string
	Kind         string // "*" matches all kinds in that group/version
	RemovedIn    int    // Kubernetes minor version (e.g. 25 for 1.25, 32 for 1.32)
	DeprecatedIn int
	Replacement  string
}

// Deterministic Kubernetes API removal & deprecation table covering K8s 1.22 -> 1.36+.
var removalTable = []apiRemovalRule{
	{APIVersion: "extensions/v1beta1", Kind: "*", RemovedIn: 22, DeprecatedIn: 16, Replacement: "apps/v1 or networking.k8s.io/v1"},
	{APIVersion: "networking.k8s.io/v1beta1", Kind: "Ingress", RemovedIn: 22, DeprecatedIn: 19, Replacement: "networking.k8s.io/v1"},
	{APIVersion: "networking.k8s.io/v1beta1", Kind: "IngressClass", RemovedIn: 22, DeprecatedIn: 19, Replacement: "networking.k8s.io/v1"},
	{APIVersion: "rbac.authorization.k8s.io/v1beta1", Kind: "*", RemovedIn: 22, DeprecatedIn: 17, Replacement: "rbac.authorization.k8s.io/v1"},
	{APIVersion: "apiextensions.k8s.io/v1beta1", Kind: "CustomResourceDefinition", RemovedIn: 22, DeprecatedIn: 16, Replacement: "apiextensions.k8s.io/v1"},
	{APIVersion: "admissionregistration.k8s.io/v1beta1", Kind: "*", RemovedIn: 22, DeprecatedIn: 16, Replacement: "admissionregistration.k8s.io/v1"},
	{APIVersion: "batch/v1beta1", Kind: "CronJob", RemovedIn: 25, DeprecatedIn: 21, Replacement: "batch/v1"},
	{APIVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", RemovedIn: 25, DeprecatedIn: 21, Replacement: "policy/v1"},
	{APIVersion: "policy/v1beta1", Kind: "PodSecurityPolicy", RemovedIn: 25, DeprecatedIn: 21, Replacement: "Pod Security Admission / ValidatingAdmissionPolicy"},
	{APIVersion: "autoscaling/v2beta1", Kind: "HorizontalPodAutoscaler", RemovedIn: 25, DeprecatedIn: 19, Replacement: "autoscaling/v2"},
	{APIVersion: "autoscaling/v2beta2", Kind: "HorizontalPodAutoscaler", RemovedIn: 26, DeprecatedIn: 23, Replacement: "autoscaling/v2"},
	{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta1", Kind: "*", RemovedIn: 26, DeprecatedIn: 23, Replacement: "flowcontrol.apiserver.k8s.io/v1"},
	{APIVersion: "storage.k8s.io/v1beta1", Kind: "CSIStorageCapacity", RemovedIn: 27, DeprecatedIn: 24, Replacement: "storage.k8s.io/v1"},
	{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta2", Kind: "*", RemovedIn: 29, DeprecatedIn: 26, Replacement: "flowcontrol.apiserver.k8s.io/v1"},
	{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "*", RemovedIn: 32, DeprecatedIn: 29, Replacement: "flowcontrol.apiserver.k8s.io/v1"},
	{APIVersion: "admissionregistration.k8s.io/v1beta1", Kind: "ValidatingAdmissionPolicy", RemovedIn: 34, DeprecatedIn: 30, Replacement: "admissionregistration.k8s.io/v1"},
	{APIVersion: "admissionregistration.k8s.io/v1beta1", Kind: "ValidatingAdmissionPolicyBinding", RemovedIn: 34, DeprecatedIn: 30, Replacement: "admissionregistration.k8s.io/v1"},
	{APIVersion: "networking.k8s.io/v1alpha1", Kind: "ClusterCIDR", RemovedIn: 31, DeprecatedIn: 29, Replacement: "networking.k8s.io/v1 ServiceCIDR"},
}

var coreAPIGroups = map[string]bool{
	"v1":                           true,
	"apps":                         true,
	"batch":                        true,
	"networking.k8s.io":            true,
	"policy":                       true,
	"rbac.authorization.k8s.io":    true,
	"autoscaling":                  true,
	"storage.k8s.io":               true,
	"apiextensions.k8s.io":         true,
	"admissionregistration.k8s.io": true,
	"certificates.k8s.io":          true,
	"coordination.k8s.io":          true,
	"discovery.k8s.io":             true,
	"events.k8s.io":                true,
	"scheduling.k8s.io":            true,
	"node.k8s.io":                  true,
	"flowcontrol.apiserver.k8s.io": true,
	"apiregistration.k8s.io":       true,
	"authentication.k8s.io":        true,
	"authorization.k8s.io":         true,
	"resource.k8s.io":              true,
	"internal.apiserver.k8s.io":    true,
}

// ParseManifestStream parses multi-document rendered Kubernetes YAML into structured objects.
func ParseManifestStream(renderedYAML string) ([]ManifestObject, error) {
	dec := yaml.NewDecoder(strings.NewReader(renderedYAML))
	var out []ManifestObject
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("parse rendered manifest YAML: %w", err)
		}
		if node.Kind == 0 || (node.Kind == yaml.DocumentNode && len(node.Content) == 0) {
			continue
		}
		rawDoc, _ := yaml.Marshal(&node)
		if string(rawDoc) == "null\n" || strings.TrimSpace(string(rawDoc)) == "" {
			continue
		}
		var obj ManifestObject
		if err := node.Decode(&obj); err != nil {
			continue
		}
		if obj.APIVersion == "" || obj.Kind == "" {
			continue
		}
		var full map[string]any
		if err := node.Decode(&full); err == nil {
			obj.Object = full
		}
		obj.RawBytes = len(rawDoc)
		obj.RawYAML = string(rawDoc)
		out = append(out, obj)
	}
	return out, nil
}

func ParseMinorVersion(k8sVersion string) int {
	v := strings.TrimPrefix(strings.TrimSpace(k8sVersion), "v")
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return 33
	}
	minorStr := parts[1]
	for i := 0; i < len(minorStr); i++ {
		if minorStr[i] < '0' || minorStr[i] > '9' {
			minorStr = minorStr[:i]
			break
		}
	}
	m, err := strconv.Atoi(minorStr)
	if err != nil {
		return 33
	}
	return m
}

// CheckCompatibility validates rendered objects against targetK8s and optional nextEOLK8s,
// plus client-side 256KiB annotation size traps on CRDs when serverSideApply is false.
func CheckCompatibility(objs []ManifestObject, targetK8s, nextEOLK8s string, serverSideApply bool) []verdict.Finding {
	targetMinor := ParseMinorVersion(targetK8s)
	nextMinor := 0
	if nextEOLK8s != "" {
		nextMinor = ParseMinorVersion(nextEOLK8s)
	}

	var findings []verdict.Finding
	for _, obj := range objs {
		for _, rule := range removalRulesFor(obj) {
			if targetMinor >= rule.RemovedIn {
				findings = append(findings, verdict.Finding{
					RuleID:   "k8s.api.removed",
					Severity: verdict.SeverityBlocker,
					Resource: obj.ID(),
					Message:  fmt.Sprintf("API `%s` (%s) was removed in Kubernetes 1.%d (target is %s).", obj.APIVersion, obj.Kind, rule.RemovedIn, targetK8s),
					Detail:   fmt.Sprintf("Migrate resource to `%s`.", rule.Replacement),
				})
			} else if nextMinor >= rule.RemovedIn {
				findings = append(findings, verdict.Finding{
					RuleID:   "k8s.api.eol_blocker",
					Severity: verdict.SeverityWarning,
					Resource: obj.ID(),
					Message:  fmt.Sprintf("API `%s` (%s) works on %s, but is REMOVED in next EOL target Kubernetes 1.%d.", obj.APIVersion, obj.Kind, targetK8s, rule.RemovedIn),
					Detail:   fmt.Sprintf("Migrate to `%s` before upgrading cluster to %s.", rule.Replacement, nextEOLK8s),
				})
			} else if targetMinor >= rule.DeprecatedIn {
				findings = append(findings, verdict.Finding{
					RuleID:   "k8s.api.deprecated",
					Severity: verdict.SeverityWarning,
					Resource: obj.ID(),
					Message:  fmt.Sprintf("API `%s` (%s) is deprecated since 1.%d and removed in 1.%d.", obj.APIVersion, obj.Kind, rule.DeprecatedIn, rule.RemovedIn),
					Detail:   fmt.Sprintf("Recommended replacement: `%s`.", rule.Replacement),
				})
			}
		}

		// Oversized CRD (>256KiB) breaks kubectl / Argo CD / Terraform client-side `last-applied-configuration` annotations
		// unless Server-Side Apply (`--server-side-apply` / `ServerSideApply=true`) is enabled in the deployment tool.
		if !serverSideApply && obj.Kind == "CustomResourceDefinition" && obj.RawBytes > 262144 {
			findings = append(findings, verdict.Finding{
				RuleID:   "k8s.crd.oversized_annotation_limit",
				Severity: verdict.SeverityWarning,
				Resource: obj.ID(),
				Message:  fmt.Sprintf("CRD size is %d KiB (>256 KiB client-side `last-applied-configuration` limit).", obj.RawBytes/1024),
				Detail:   "1-line fixes -> Argo CD: `syncOptions: [ServerSideApply=true]` | Flux: `install/upgrade.crds: CreateReplace` | Helm v4: `helm upgrade --server-side` | kubectl: `kubectl apply --server-side -f crds/` (pass `--server-side-apply` if already enabled).",
			})
		}
	}
	return findings
}

// removalRulesFor returns the removal rules that apply to obj. A rule that names
// the kind wins over a wildcard rule for the same API version. A group/version
// can gain a kind long after its first kinds were removed: ValidatingAdmissionPolicy
// arrived in admissionregistration.k8s.io/v1beta1 in 1.28, years after the webhook
// kinds left that version in 1.22. The wildcard alone would date it to 1.22.
func removalRulesFor(obj ManifestObject) []apiRemovalRule {
	var exact, wildcard []apiRemovalRule
	for _, rule := range removalTable {
		if rule.APIVersion != obj.APIVersion {
			continue
		}
		switch rule.Kind {
		case obj.Kind:
			exact = append(exact, rule)
		case "*":
			wildcard = append(wildcard, rule)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return wildcard
}

// DiffUpgradeSafety checks immutable field mutations between base and head rendered manifests
// (e.g., changing `spec.selector.matchLabels` on a Deployment/StatefulSet/DaemonSet breaks in-place `helm upgrade`!).
func DiffUpgradeSafety(baseObjs, headObjs []ManifestObject) []verdict.Finding {
	baseMap := make(map[string]ManifestObject, len(baseObjs))
	for _, b := range baseObjs {
		baseMap[b.matchKey()] = b
	}

	var findings []verdict.Finding
	for _, h := range headObjs {
		b, exists := baseMap[h.matchKey()]
		if !exists {
			continue
		}
		if h.Kind == "Deployment" || h.Kind == "StatefulSet" || h.Kind == "DaemonSet" {
			bSel := extractMatchLabels(b.Spec)
			hSel := extractMatchLabels(h.Spec)
			if len(bSel) > 0 && len(hSel) > 0 && !equalStringMap(bSel, hSel) {
				findings = append(findings, verdict.Finding{
					RuleID:   "k8s.upgrade.immutable_selector_changed",
					Severity: verdict.SeverityBlocker,
					Resource: h.ID(),
					Message:  fmt.Sprintf("Immutable `spec.selector.matchLabels` changed from %v to %v. An in-place `helm upgrade` will fail.", bSel, hSel),
					Detail:   "Kubernetes forbids mutating Deployment/StatefulSet/DaemonSet label selectors in-place without recreating the workload.",
				})
			}
		}
	}
	return findings
}

// DetectExternalCRDs returns custom `apiVersion/Kind` resources referenced inobjs
// whose CRDs are NOT included inside the rendered manifest stream itself.
func DetectExternalCRDs(objs []ManifestObject) []string {
	definedKinds := make(map[string]bool)
	for _, o := range objs {
		if o.Kind == "CustomResourceDefinition" {
			group, _ := o.Spec["group"].(string)
			names, _ := o.Spec["names"].(map[string]any)
			kind, _ := names["kind"].(string)
			if group != "" && kind != "" {
				definedKinds[group+"/"+kind] = true
			}
		}
	}

	seen := make(map[string]bool)
	var external []string
	for _, o := range objs {
		group := apiGroup(o.APIVersion)
		if coreAPIGroups[group] {
			continue
		}
		gk := group + "/" + o.Kind
		if !definedKinds[gk] && !seen[gk] {
			seen[gk] = true
			external = append(external, gk)
		}
	}
	return external
}

func apiGroup(apiVersion string) string {
	idx := strings.Index(apiVersion, "/")
	if idx == -1 {
		return apiVersion // "v1"
	}
	return apiVersion[:idx]
}

func extractMatchLabels(spec map[string]any) map[string]string {
	sel, ok := spec["selector"].(map[string]any)
	if !ok {
		return nil
	}
	ml, ok := sel["matchLabels"].(map[string]any)
	if !ok {
		return nil
	}
	res := make(map[string]string, len(ml))
	for k, v := range ml {
		res[k] = fmt.Sprint(v)
	}
	return res
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
