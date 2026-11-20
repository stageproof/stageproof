package argocd

import (
	"strings"
	"testing"
)

func appYAML(repo, values string) []byte {
	return []byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  source:
    repoURL: ` + repo + `
    chart: demo
    targetRevision: 1.2.3
    helm:
      values: |
        ` + values + `
`)
}

func TestUnresolvedChartTokenIsNamed(t *testing.T) {
	_, _, err := ParseApplicationBytes(appYAML("https://charts.example/${repo}", "size: small"), "demo.yaml", nil)
	tokens := UnresolvedTokens(err)
	if len(tokens) != 1 || tokens[0] != "${repo}" {
		t.Fatalf("tokens %v err %v", tokens, err)
	}
}

func TestConfiguredTokenIsReplaced(t *testing.T) {
	spec, used, err := ParseApplicationBytes(appYAML("https://charts.example", "size: ${size}"), "demo.yaml", map[string]string{"${size}": "10Gi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 0 {
		t.Fatalf("configured token should not be reported, got %v", used)
	}
	if !strings.Contains(spec.ValuesYAML, "10Gi") {
		t.Fatalf("values: %s", spec.ValuesYAML)
	}
}

func TestOtherTokenBecomesTheFixedPlaceholder(t *testing.T) {
	spec, used, err := ParseApplicationBytes(appYAML("https://charts.example", "size: ${size}"), "demo.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "${size}" {
		t.Fatalf("used %v", used)
	}
	if !strings.Contains(spec.ValuesYAML, "stageproof-placeholder") {
		t.Fatalf("values: %s", spec.ValuesYAML)
	}
	if strings.Contains(spec.RepoURL, "stageproof-placeholder") || strings.Contains(spec.Chart, "stageproof-placeholder") {
		t.Fatalf("the chart source should stay intact: %+v", spec)
	}
}
