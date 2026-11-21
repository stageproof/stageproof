package impact

import (
	"bytes"
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
		StoredBefore: [][]byte{stored},
		StoredAfter:  [][]byte{stored},
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

func TestClusterFactsStoredVersion(t *testing.T) {
	raw := `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names:
    kind: Widget
    plural: widgets
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
    - name: v1beta1
      served: true
      storage: false
      schema:
        openAPIV3Schema:
          type: object
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.com
spec:
  group: example.com
  scope: Namespaced
  names:
    kind: Widget
    plural: widgets
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
`
	objs, err := k8sschema.ParseManifestStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 2 {
		t.Fatalf("docs: %d", len(objs))
	}
	quiet, _ := Compare(objs[:1], objs[1:], Options{})
	rejectRule(t, quiet, "crd.existing_object_invalid")
	hit, _ := Compare(objs[:1], objs[1:], Options{ClusterStored: map[string][]string{
		"widgets.example.com": {"v1beta1"},
	}})
	requireRule(t, hit, "crd.existing_object_invalid", verdict.SeverityBlocker)
	if !hasMessage(hit, "crd.existing_object_invalid", "v1beta1") {
		t.Fatalf("stored version was not named: %+v", hit)
	}
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
	if !hasMessage(argo, "crd.argocd_renders_crds", "does not say whether skipCrds is set") {
		t.Fatalf("absent skipCrds text missing: %+v", argo)
	}

	off := false
	known, _ := Compare(nil, nil, Options{
		Adapter:      "argocd",
		ArgoSkipCrds: &off,
		BeforeDir:    filepath.Join(root, "break", "before"),
		AfterDir:     filepath.Join(root, "break", "after"),
	})
	requireRule(t, known, "crd.argocd_renders_crds", verdict.SeverityWarning)
	if !hasMessage(known, "crd.argocd_renders_crds", "skipCrds is false") {
		t.Fatalf("explicit false text missing: %+v", known)
	}
	if hasMessage(known, "crd.argocd_renders_crds", "does not say") {
		t.Fatalf("explicit false reused the unknown sentence: %+v", known)
	}

	skip := true
	skipped, _ := Compare(nil, nil, Options{
		Adapter:      "argocd",
		ArgoSkipCrds: &skip,
		BeforeDir:    filepath.Join(root, "break", "before"),
		AfterDir:     filepath.Join(root, "break", "after"),
	})
	requireRule(t, skipped, "crd.not_upgraded_by_helm", verdict.SeverityBlocker)
	rejectRule(t, skipped, "crd.argocd_renders_crds")
	if !hasMessage(skipped, "crd.not_upgraded_by_helm", "skipCrds is true") {
		t.Fatalf("explicit true text missing: %+v", skipped)
	}

	flux, _ := Compare(nil, nil, Options{
		Adapter:   "flux",
		BeforeDir: filepath.Join(root, "break", "before"),
		AfterDir:  filepath.Join(root, "break", "after"),
	})
	requireRule(t, flux, "crd.flux_upgrade_crds", verdict.SeverityBlocker)
	if !hasMessage(findings, "crd.not_upgraded_by_helm", "Plain Helm does not upgrade") {
		t.Fatalf("helm text missing: %+v", findings)
	}
	if !hasMessage(flux, "crd.flux_upgrade_crds", "defaults to Skip") {
		t.Fatalf("flux default text missing: %+v", flux)
	}
	explicit, _ := Compare(nil, nil, Options{
		Adapter:       "flux",
		FluxCRDPolicy: "Skip",
		BeforeDir:     filepath.Join(root, "break", "before"),
		AfterDir:      filepath.Join(root, "break", "after"),
	})
	requireRule(t, explicit, "crd.flux_upgrade_crds", verdict.SeverityBlocker)
	if !hasMessage(explicit, "crd.flux_upgrade_crds", "spec.upgrade.crds is Skip") || hasMessage(explicit, "crd.flux_upgrade_crds", "omitted") {
		t.Fatalf("explicit Skip text: %+v", explicit)
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

func TestHooksAreNotDeletesOrRestarts(t *testing.T) {
	before := parseDocs(t, `
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  annotations: {"helm.sh/hook": pre-upgrade}
spec:
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: busybox:1.36}]
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: scratch
  annotations: {"helm.sh/hook": pre-upgrade}
spec:
  accessModes: ["ReadWriteOnce"]
  resources: {requests: {storage: 1Gi}}
`)
	after := parseDocs(t, `
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  annotations: {"helm.sh/hook": pre-upgrade}
spec:
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: busybox:1.37}]
`)
	findings, _ := Compare(before, after, Options{})
	rejectRule(t, findings, "impact.delete")
	rejectRule(t, findings, "impact.delete.pvc")
	rejectRule(t, findings, "impact.restart")
}

func TestRemovedSecurityContextIsWeaker(t *testing.T) {
	before, after := split(t, "privilege/context-removed.yaml", 1)
	findings, _ := Compare(before, after, Options{})
	text := joined(findings, "impact.privilege.securitycontext")
	for _, field := range []string{"runAsNonRoot", "readOnlyRootFilesystem", "allowPrivilegeEscalation", "capabilities.drop"} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %s in %s", field, text)
		}
	}
	quiet, _ := Compare(before, before, Options{})
	rejectRule(t, quiet, "impact.privilege.securitycontext")
}

func TestCompareDeterministic(t *testing.T) {
	before := parseDocs(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: b}
spec:
  selector: {matchLabels: {app: b}}
  template:
    metadata: {labels: {app: b}}
    spec: {containers: [{name: app, image: nginx:1.27}]}
---
apiVersion: v1
kind: Service
metadata: {name: a}
spec: {type: ClusterIP, ports: [{port: 80, name: http}]}
`)
	after := parseDocs(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: b}
spec:
  selector: {matchLabels: {app: b}}
  template:
    metadata: {labels: {app: b}}
    spec: {containers: [{name: app, image: nginx:1.28}]}
---
apiVersion: v1
kind: Service
metadata: {name: a}
spec: {type: LoadBalancer, ports: [{port: 80, name: http}]}
`)
	var first []byte
	for i := 0; i < 20; i++ {
		findings, summary := Compare(before, after, Options{})
		raw, err := json.Marshal(struct {
			Findings []verdict.Finding `json:"findings"`
			Summary  string            `json:"summary"`
		}{findings, summary})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = raw
			continue
		}
		if !bytes.Equal(first, raw) {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestCRDAccuracy(t *testing.T) {
	base := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: string}}}}}}
    - name: v1alpha1
      served: true
      storage: false
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: string}}}}}}
`)
	changed := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: string}}}}}}
    - name: v1alpha1
      served: true
      storage: false
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: integer}}}}}}
`)
	widget := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo}\nspec: {size: large}\n")
	findings, _ := Compare(append(base, widget...), append(changed, widget...), Options{})
	requireRule(t, findings, "crd.schema.incompatible", verdict.SeverityWarning)
	rejectSeverity(t, findings, "crd.existing_object_invalid", verdict.SeverityBlocker)

	oldReq := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {color: {type: string}, size: {type: string}}}}}}
`)
	newReq := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, required: [color], properties: {color: {type: string}, size: {type: string}}}}}}
`)
	set := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo}\nspec: {color: blue, size: large}\n")
	kept, _ := Compare(append(oldReq, set...), append(newReq, set...), Options{})
	requireRule(t, kept, "crd.schema.incompatible", verdict.SeverityWarning)
	rejectSeverity(t, kept, "crd.existing_object_invalid", verdict.SeverityBlocker)
	missing := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo}\nspec: {size: large}\n")
	missed, _ := Compare(append(oldReq, missing...), append(newReq, missing...), Options{})
	requireRule(t, missed, "crd.existing_object_invalid", verdict.SeverityBlocker)

	oldEnum := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {color: {type: string, enum: [kept, dropped-token]}}}}}}
`)
	newEnum := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {color: {type: string, enum: [kept]}}}}}}
`)
	still := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo}\nspec: {color: kept}\n")
	narrow, _ := Compare(append(oldEnum, still...), append(newEnum, still...), Options{})
	requireRule(t, narrow, "crd.schema.incompatible", verdict.SeverityWarning)
	rejectSeverity(t, narrow, "crd.existing_object_invalid", verdict.SeverityBlocker)
	gone := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo}\nspec: {color: dropped-token}\n")
	removed, _ := Compare(append(oldEnum, gone...), append(newEnum, gone...), Options{})
	requireRule(t, removed, "crd.existing_object_invalid", verdict.SeverityBlocker)
	if strings.Contains(joined(removed, "crd.existing_object_invalid"), "dropped-token") {
		t.Fatalf("enum value leaked: %+v", removed)
	}

	a := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo-a}\nspec: {size: large}\n")
	b := parseDocs(t, "apiVersion: example.com/v1\nkind: Widget\nmetadata: {name: demo-b}\nspec: {size: large}\n")
	oldType := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: string}}}}}}
`)
	newType := crdWith(t, `
    - name: v1
      served: true
      storage: true
      schema: {openAPIV3Schema: {type: object, properties: {spec: {type: object, properties: {size: {type: integer}}}}}}
`)
	both, _ := Compare(append(append(oldType, a...), b...), append(append(newType, a...), b...), Options{})
	var hits []string
	for _, f := range both {
		if f.RuleID == "crd.existing_object_invalid" {
			hits = append(hits, f.Resource)
		}
	}
	if strings.Join(hits, ",") != "Widget/default/demo-a,Widget/default/demo-b" {
		t.Fatalf("both custom resources must be named: %v", hits)
	}
}

func TestVersionAndProperty(t *testing.T) {
	ver, prop := versionAndProperty("removed field : v1alpha1.^.spec.size")
	if ver != "v1alpha1" || prop != "^.spec.size" {
		t.Fatalf("got %s %s", ver, prop)
	}
	got := listFromDetail(`new required fields: [color size]`)
	if strings.Join(got, ",") != "color,size" {
		t.Fatalf("required list: %#v", got)
	}
	got = listFromDetail(`allowed enum values removed : ["dropped-token"]`)
	if strings.Join(got, ",") != "dropped-token" {
		t.Fatalf("enum list: %#v", got)
	}
}

func TestLoadDirCRDsWalksNested(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "crds", "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "charts", "extra", "crds")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: widgets.example.com}\nspec: {group: example.com, names: {kind: Widget, plural: widgets}}\n")
	if err := os.WriteFile(filepath.Join(nested, "widgets.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	other := []byte("apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata: {name: gadgets.example.com}\nspec: {group: example.com, names: {kind: Gadget, plural: gadgets}}\n")
	if err := os.WriteFile(filepath.Join(sub, "gadgets.yaml"), other, 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadDirCRDs(dir)
	if _, ok := got["widgets.example.com"]; !ok {
		t.Fatalf("nested crds/ file missing: %v", got)
	}
	if _, ok := got["gadgets.example.com"]; !ok {
		t.Fatalf("subchart crds/ file missing: %v", got)
	}
}

func parseDocs(t *testing.T, raw string) []k8sschema.ManifestObject {
	t.Helper()
	objs, err := k8sschema.ParseManifestStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	return objs
}

func crdWith(t *testing.T, versions string) []k8sschema.ManifestObject {
	t.Helper()
	return parseDocs(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.com}
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget, plural: widgets}
  versions:
`+versions)
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
