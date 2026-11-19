package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
	"gopkg.in/yaml.v3"
)

func TestAssignmentsYAML_NestedParameter(t *testing.T) {
	got, err := AssignmentsYAML([]string{"preUpgradeChecker.jobEnabled=false", "service.ui.type=LoadBalancer"})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatal(err)
	}
	pre, _ := doc["preUpgradeChecker"].(map[string]any)
	if pre["jobEnabled"] != "false" {
		t.Fatalf("jobEnabled = %#v", pre["jobEnabled"])
	}
	svc, _ := doc["service"].(map[string]any)
	ui, _ := svc["ui"].(map[string]any)
	if ui["type"] != "LoadBalancer" {
		t.Fatalf("type = %#v", ui["type"])
	}
}

func TestVersionSignals_SkipAndPatch(t *testing.T) {
	skip := versionSignals(chartDoc{Version: "1.7.2", AppVersion: "v1.7.2"}, chartDoc{Version: "1.13.0", AppVersion: "v1.13.0"})
	if len(skip) != 2 || skip[0].RuleID != "helm.release.version_span" || skip[1].RuleID != "helm.release.app_version_changed" {
		t.Fatalf("skip signals = %+v", skip)
	}
	if !strings.Contains(skip[0].Message, "1.7.2") || !strings.Contains(skip[0].Message, "1.13.0") {
		t.Fatalf("span message = %s", skip[0].Message)
	}
	patch := versionSignals(chartDoc{Version: "10.18.0", AppVersion: "v3.6.1"}, chartDoc{Version: "10.18.1", AppVersion: "v3.6.1"})
	if len(patch) != 0 {
		t.Fatalf("patch bump should be silent, got %+v", patch)
	}
	oneMinor := versionSignals(chartDoc{Version: "1.7.2", AppVersion: "v1.7.2"}, chartDoc{Version: "1.8.0", AppVersion: "v1.8.0"})
	if len(oneMinor) != 1 || oneMinor[0].RuleID != "helm.release.app_version_changed" {
		t.Fatalf("one minor step should name appVersion only, got %+v", oneMinor)
	}
}

func TestDependencySignals_VendoredChartUnchanged(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	head := filepath.Join(root, "head")
	for _, dir := range []string{base, head} {
		if err := os.MkdirAll(filepath.Join(dir, "charts", "grafana"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "charts", "grafana", "Chart.yaml"), []byte("version: 6.29.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "Chart.yaml"), []byte("version: 35.3.1\ndependencies:\n- name: grafana\n  version: 6.29.*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(head, "Chart.yaml"), []byte("version: 35.3.1\ndependencies:\n- name: grafana\n  version: 6.61.*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := dependencySignals(base, head, readChartDoc(base), readChartDoc(head))
	if len(got) != 1 || got[0].RuleID != "helm.dependency.not_updated" || !strings.Contains(got[0].Message, "grafana 6.29.* to 6.61.*") {
		t.Fatalf("got %+v", got)
	}
}

func TestImageSignals_TagChangeAndVPrefix(t *testing.T) {
	base := []k8sschema.ManifestObject{{
		Kind:     "Deployment",
		Metadata: k8sschema.ObjectMetadata{Name: "web", Namespace: "sites"},
		Spec: map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "wordpress", "image": "docker.io/wordpress:7.1.4-php8.3-apache"}},
				},
			},
		},
	}}
	head := []k8sschema.ManifestObject{{
		Kind:     "Deployment",
		Metadata: k8sschema.ObjectMetadata{Name: "web", Namespace: "sites"},
		Spec: map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "wordpress", "image": "docker.io/wordpress:7.1.5-php8.3-apache"}},
				},
			},
		},
	}}
	got := imageSignals(base, head)
	if len(got) != 1 || !strings.Contains(got[0].Message, "7.1.4-php8.3-apache") || !strings.Contains(got[0].Message, "7.1.5-php8.3-apache") {
		t.Fatalf("got %+v", got)
	}

	base[0].Spec = map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "controller", "image": "docker.io/bitnami/sealed-secrets-controller:v0.18.0"}},
	}}}
	head[0].Spec = map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "controller", "image": "docker.io/bitnami/sealed-secrets-controller:0.18.0"}},
	}}}
	got = imageSignals(base, head)
	if len(got) != 1 || !strings.Contains(got[0].Message, "leading v") {
		t.Fatalf("v prefix note missing: %+v", got)
	}
}

func TestImageSignals_CustomResourceFieldAndArgument(t *testing.T) {
	base := []k8sschema.ManifestObject{
		{
			Kind:     "Alertmanager",
			Metadata: k8sschema.ObjectMetadata{Name: "am", Namespace: "mon"},
			Spec:     map[string]any{"image": "quay.io/prometheus/alertmanager:v0.34.0"},
		},
		{
			Kind:     "Deployment",
			Metadata: k8sschema.ObjectMetadata{Name: "op", Namespace: "mon"},
			Spec: map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{
					"name":  "operator",
					"image": "quay.io/prometheus-operator/prometheus-operator:v0.70.0",
					"args":  []any{"--prometheus-config-reloader=quay.io/prometheus-operator/prometheus-config-reloader:v0.70.0"},
				}},
			}}},
		},
	}
	head := []k8sschema.ManifestObject{
		{
			Kind:     "Alertmanager",
			Metadata: k8sschema.ObjectMetadata{Name: "am", Namespace: "mon"},
			Spec:     map[string]any{"image": "quay.io/prometheus/alertmanager:v0.35.0"},
		},
		{
			Kind:     "Deployment",
			Metadata: k8sschema.ObjectMetadata{Name: "op", Namespace: "mon"},
			Spec: map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{map[string]any{
					"name":  "operator",
					"image": "quay.io/prometheus-operator/prometheus-operator:v0.95.2",
					"args":  []any{"--prometheus-config-reloader=quay.io/prometheus-operator/prometheus-config-reloader:v0.95.2"},
				}},
			}}},
		},
	}
	got := imageSignals(base, head)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	for _, want := range []string{"alertmanager:v0.34.0", "alertmanager:v0.35.0", "prometheus-operator:v0.70.0", "prometheus-operator:v0.95.2", "prometheus-config-reloader:v0.70.0", "prometheus-config-reloader:v0.95.2"} {
		if !strings.Contains(got[0].Message, want) {
			t.Fatalf("missing %s in %s", want, got[0].Message)
		}
	}
}

func TestImageSignals_RawYAMLFindsCustomResourceAndArgument(t *testing.T) {
	rawBase := `
apiVersion: monitoring.coreos.com/v1
kind: Prometheus
metadata:
  name: kps
  namespace: mon
spec:
  image: quay.io/prometheus/prometheus:v2.35.0
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: operator
  namespace: mon
spec:
  template:
    spec:
      containers:
      - name: operator
        image: quay.io/prometheus-operator/prometheus-operator:v0.60.0
        args:
        - --thanos-default-base-image=quay.io/thanos/thanos:v0.25.2
      - name: patch
        image: registry.k8s.io/ingress-nginx/kube-webhook-certgen:v1.1.1
      - name: curl
        image: curlimages/curl:7.88.1
`
	rawHead := strings.ReplaceAll(rawBase, "v2.35.0", "v2.55.1")
	rawHead = strings.ReplaceAll(rawHead, "v0.25.2", "v0.42.5")
	rawHead = strings.ReplaceAll(rawHead, "v1.1.1", "v1.6.9")
	rawHead = strings.ReplaceAll(rawHead, "7.88.1", "8.23.0")
	base, err := k8sschema.ParseManifestStream(rawBase)
	if err != nil {
		t.Fatal(err)
	}
	head, err := k8sschema.ParseManifestStream(rawHead)
	if err != nil {
		t.Fatal(err)
	}
	got := imageSignals(base, head)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	for _, want := range []string{"prometheus:v2.35.0", "prometheus:v2.55.1", "thanos:v0.25.2", "thanos:v0.42.5", "kube-webhook-certgen:v1.1.1", "kube-webhook-certgen:v1.6.9", "curl:7.88.1", "curl:8.23.0"} {
		if !strings.Contains(got[0].Message, want) {
			t.Fatalf("missing %s in %s", want, got[0].Message)
		}
	}
}

func TestOperatorSignal_NamesExistingCustomResources(t *testing.T) {
	root := t.TempDir()
	for _, side := range []string{"base", "head"} {
		dir := filepath.Join(root, side, "crds")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cluster.yaml"), []byte("kind: CustomResourceDefinition\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prior := []verdict.Finding{{RuleID: "helm.release.app_version_changed", Severity: verdict.SeverityWarning}}
	got := operatorSignal(filepath.Join(root, "base"), filepath.Join(root, "head"), prior)
	if len(got) != 1 || got[0].RuleID != "helm.operator.existing_custom_resources" {
		t.Fatalf("got %+v", got)
	}
	if operatorSignal(filepath.Join(root, "base"), filepath.Join(root, "head"), nil) != nil {
		t.Fatal("no application change should stay quiet")
	}
}
