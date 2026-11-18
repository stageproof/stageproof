package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/k8sschema"
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
		Kind: "Deployment",
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
		Kind: "Deployment",
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
