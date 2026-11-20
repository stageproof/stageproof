package impact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestDeletes(t *testing.T) {
	before := load(t, "deletes/break-before.yaml")
	after := load(t, "deletes/break-after.yaml")
	findings, _ := Compare(before, after, Options{})
	requireRule(t, findings, "impact.delete.crd", verdict.SeverityBlocker)
	requireRule(t, findings, "impact.delete.pvc", verdict.SeverityBlocker)
	requireRule(t, findings, "impact.delete.namespace", verdict.SeverityBlocker)
	requireRule(t, findings, "impact.delete.statefulset", verdict.SeverityBlocker)
	if !hasMessage(findings, "impact.delete", "replaced: delete then create") {
		t.Fatalf("a renamed object must say replaced: delete then create: %+v", findings)
	}
	if !hasMessage(findings, "impact.delete", "ConfigMap/default/cfg") {
		t.Fatalf("other deletes are one warning: %+v", findings)
	}

	same := load(t, "deletes/control-before.yaml")
	quiet, _ := Compare(same, same, Options{})
	rejectRule(t, quiet, "impact.delete.crd")
	rejectRule(t, quiet, "impact.delete.pvc")
	rejectRule(t, quiet, "impact.delete.statefulset")

	var kept []k8sschema.ManifestObject
	for _, o := range same {
		if o.Metadata.Annotations["helm.sh/resource-policy"] == "keep" {
			kept = append(kept, o)
		}
	}
	gone, _ := Compare(kept, nil, Options{})
	rejectRule(t, gone, "impact.delete.crd")
	rejectRule(t, gone, "impact.delete.pvc")

	var sts []k8sschema.ManifestObject
	for _, o := range same {
		if o.Kind == "StatefulSet" {
			sts = append(sts, o)
		}
	}
	warn, _ := Compare(sts, nil, Options{})
	requireRule(t, warn, "impact.delete.statefulset", verdict.SeverityWarning)
	rejectSeverity(t, warn, "impact.delete.statefulset", verdict.SeverityBlocker)
}

func TestRestarts(t *testing.T) {
	before, after := split(t, "restarts/break.yaml", 1)
	findings, summary := Compare(before, after, Options{})
	requireRule(t, findings, "impact.restart", verdict.SeverityInfo)
	if !hasMessage(findings, "impact.restart", "helm.sh/chart") {
		t.Fatalf("the restart must name the field: %+v", findings)
	}
	if strings.Contains(joined(findings, "impact.restart"), "ERROR") {
		t.Fatal("a restart is not a problem")
	}
	if !strings.Contains(summary, "API server did not run") {
		t.Fatalf("raw comparison must say so: %s", summary)
	}
	quiet, _ := Compare(before, before, Options{})
	rejectRule(t, quiet, "impact.restart")

	reb, rea := split(t, "restarts/reorder.yaml", 1)
	order, _ := Compare(reb, rea, Options{})
	rejectRule(t, order, "impact.restart")
}

func TestStoredFormsIgnoreServerDefaults(t *testing.T) {
	raw := load(t, "restarts/reorder.yaml")
	before := raw[0]
	after := cloneManifest(t, before)
	spec := after.Object["spec"].(map[string]any)
	tmpl := spec["template"].(map[string]any)
	ps := tmpl["spec"].(map[string]any)
	ps["terminationGracePeriodSeconds"] = 30
	stored, err := json.Marshal(before.Object)
	if err != nil {
		t.Fatal(err)
	}
	// Both stored copies match the rendered before object. The raw after
	// object carries a default the server would add. That is not a change.
	findings, summary := Compare([]k8sschema.ManifestObject{before}, []k8sschema.ManifestObject{after}, Options{
		APIServerRan: true,
		Stored:       [][]byte{stored, stored},
	})
	rejectRule(t, findings, "impact.restart")
	if !strings.Contains(summary, "stored objects") {
		t.Fatalf("stored comparison must say so: %s", summary)
	}
}

func TestExposure(t *testing.T) {
	before, after := split(t, "exposure/break.yaml", 3)
	findings, _ := Compare(before, after, Options{})
	requireRule(t, findings, "impact.exposure.service", verdict.SeverityWarning)
	requireRule(t, findings, "impact.exposure.route", verdict.SeverityWarning)
	requireRule(t, findings, "impact.exposure.networkpolicy", verdict.SeverityWarning)
	requireRule(t, findings, "impact.exposure.host", verdict.SeverityWarning)
	quiet, _ := Compare(before, before, Options{})
	rejectPrefix(t, quiet, "impact.exposure.")
}

func TestPrivilege(t *testing.T) {
	before, after := split(t, "privilege/break.yaml", 3)
	findings, _ := Compare(before, after, Options{})
	requireRule(t, findings, "impact.privilege.rbac", verdict.SeverityWarning)
	requireRule(t, findings, "impact.privilege.binding", verdict.SeverityWarning)
	requireRule(t, findings, "impact.privilege.securitycontext", verdict.SeverityWarning)
	requireRule(t, findings, "impact.privilege.hostpath", verdict.SeverityWarning)
	requireRule(t, findings, "impact.privilege.webhook", verdict.SeverityWarning)
	requireRule(t, findings, "impact.privilege.conversion", verdict.SeverityWarning)
	if !hasMessage(findings, "impact.privilege.rbac", "wildcard") || !hasMessage(findings, "impact.privilege.rbac", "secrets") {
		t.Fatalf("rbac flags missing: %+v", findings)
	}
	quiet, _ := Compare(before, before, Options{})
	rejectPrefix(t, quiet, "impact.privilege.")

	reb, rea := split(t, "privilege/reorder.yaml", 1)
	order, _ := Compare(reb, rea, Options{})
	rejectRule(t, order, "impact.privilege.rbac")
}

func TestCapacity(t *testing.T) {
	before, after := split(t, "capacity/break.yaml", 3)
	findings, _ := Compare(before, after, Options{})
	requireRule(t, findings, "impact.capacity.replicas", verdict.SeverityInfo)
	requireRule(t, findings, "impact.capacity.requests", verdict.SeverityInfo)
	requireRule(t, findings, "impact.capacity.pdb", verdict.SeverityInfo)
	requireRule(t, findings, "impact.capacity.hpa", verdict.SeverityInfo)
	quiet, _ := Compare(before, before, Options{})
	rejectPrefix(t, quiet, "impact.capacity.")
}

func TestCRDSafety(t *testing.T) {
	usedB, usedA := split(t, "crd/type-used.yaml", 2)
	used, _ := Compare(usedB, usedA, Options{})
	requireRule(t, used, "crd.existing_object_invalid", verdict.SeverityBlocker)

	unused, _ := Compare(usedB[:1], usedA[:1], Options{})
	requireRule(t, unused, "crd.schema.incompatible", verdict.SeverityWarning)
	rejectSeverity(t, unused, "crd.existing_object_invalid", verdict.SeverityBlocker)

	storeB, storeA := split(t, "crd/storage.yaml", 1)
	stored, _ := Compare(storeB, storeA, Options{})
	requireRule(t, stored, "crd.existing_object_invalid", verdict.SeverityBlocker)

	scopeB, scopeA := split(t, "crd/scope.yaml", 1)
	scope, _ := Compare(scopeB, scopeA, Options{})
	requireRule(t, scope, "crd.scope_changed", verdict.SeverityBlocker)

	descB, descA := split(t, "crd/description.yaml", 1)
	desc, _ := Compare(descB, descA, Options{})
	rejectPrefix(t, desc, "crd.")

	same, _ := Compare(usedB, usedB, Options{})
	rejectPrefix(t, same, "crd.")
}

func TestCRDsDirectory(t *testing.T) {
	root := filepath.Join("testdata", "crds-helm")
	findings, _ := Compare(nil, nil, Options{
		BeforeDir: filepath.Join(root, "break", "before"),
		AfterDir:  filepath.Join(root, "break", "after"),
	})
	requireRule(t, findings, "crd.not_upgraded_by_helm", verdict.SeverityBlocker)

	quiet, _ := Compare(nil, nil, Options{
		BeforeDir: filepath.Join(root, "control", "before"),
		AfterDir:  filepath.Join(root, "control", "after"),
	})
	rejectRule(t, quiet, "crd.not_upgraded_by_helm")

	argo, _ := Compare(nil, nil, Options{
		Adapter:   "argocd",
		BeforeDir: filepath.Join(root, "break", "before"),
		AfterDir:  filepath.Join(root, "break", "after"),
	})
	requireRule(t, argo, "crd.argocd_renders_crds", verdict.SeverityWarning)
	rejectRule(t, argo, "crd.not_upgraded_by_helm")

	flux, _ := Compare(nil, nil, Options{
		Adapter:   "flux",
		BeforeDir: filepath.Join(root, "break", "before"),
		AfterDir:  filepath.Join(root, "break", "after"),
	})
	requireRule(t, flux, "crd.flux_upgrade_crds", verdict.SeverityBlocker)
	if !hasMessage(findings, "crd.not_upgraded_by_helm", "Plain Helm does not upgrade") {
		t.Fatalf("helm text missing: %+v", findings)
	}
	if !hasMessage(flux, "crd.flux_upgrade_crds", "Skip") {
		t.Fatalf("flux default text missing: %+v", flux)
	}
}

func load(t *testing.T, name string) []k8sschema.ManifestObject {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	objs, err := k8sschema.ParseManifestStream(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func split(t *testing.T, name string, n int) ([]k8sschema.ManifestObject, []k8sschema.ManifestObject) {
	t.Helper()
	all := load(t, name)
	if len(all) < n {
		t.Fatalf("%s has %d objects", name, len(all))
	}
	return all[:n], all[n:]
}

func cloneManifest(t *testing.T, in k8sschema.ManifestObject) k8sschema.ManifestObject {
	t.Helper()
	raw, err := json.Marshal(in.Object)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	in.Object = body
	return in
}

func requireRule(t *testing.T, findings []verdict.Finding, id string, sev verdict.Severity) {
	t.Helper()
	for _, f := range findings {
		if f.RuleID == id && f.Severity == sev {
			return
		}
	}
	t.Fatalf("missing %s %s in %+v", sev, id, findings)
}

func rejectRule(t *testing.T, findings []verdict.Finding, id string) {
	t.Helper()
	for _, f := range findings {
		if f.RuleID == id {
			t.Fatalf("%s fired: %+v", id, f)
		}
	}
}

func rejectPrefix(t *testing.T, findings []verdict.Finding, prefix string) {
	t.Helper()
	for _, f := range findings {
		if strings.HasPrefix(f.RuleID, prefix) {
			t.Fatalf("%s fired: %+v", f.RuleID, f)
		}
	}
}

func rejectSeverity(t *testing.T, findings []verdict.Finding, id string, sev verdict.Severity) {
	t.Helper()
	for _, f := range findings {
		if f.RuleID == id && f.Severity == sev {
			t.Fatalf("%s %s fired: %+v", sev, id, f)
		}
	}
}

func hasMessage(findings []verdict.Finding, id, text string) bool {
	for _, f := range findings {
		if f.RuleID == id && strings.Contains(f.Message, text) {
			return true
		}
	}
	return false
}

func joined(findings []verdict.Finding, id string) string {
	var b strings.Builder
	for _, f := range findings {
		if f.RuleID == id {
			b.WriteString(f.Message)
			b.WriteString("\n")
		}
	}
	return b.String()
}
