package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stageproof/stageproof/internal/config"
)

func TestChartVersionFromTF(t *testing.T) {
	src := `
variable "replicas" {
  default = "2"
}

variable "chart_version" {
  type        = string
  description = "argo-cd Helm chart version. 10.9.6 installs Argo CD v3.5.3."
  default     = "10.18.0"
}

variable "other" {
  default = "nope"
}
`
	got, err := config.FirstCapture(src, `variable "chart_version"[^}]*default\s*=\s*"([^"]+)"`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "10.18.0" {
		t.Fatalf("chart version = %q", got)
	}
}

func TestChartRefLabelHidesTempPath(t *testing.T) {
	if got := chartRefLabel("argo-cd", "10.18.0", filepath.Join(os.TempDir(), "argocd-new.yaml")); got != "argo-cd@10.18.0" {
		t.Fatalf("temp label = %q", got)
	}
	if got := chartRefLabel("argo-cd", "10.9.6", "charts/platform/application.yaml.tmpl"); got != "argo-cd@10.9.6 (charts/platform/application.yaml.tmpl)" {
		t.Fatalf("repo label = %q", got)
	}
}

// Reports must name where Argo CD values came from, never the temporary file
// they were written to for the check.
func TestInlineValuesAreNamedByTheirSource(t *testing.T) {
	tmp := filepath.Join(os.TempDir(), "stageproof-argopull-1", "argocd-inline-values.yaml")
	labels := map[string]string{tmp: inlineValuesLabel("apps/argo-cd.yaml")}
	got := relabel([]string{"values-prod.yaml", tmp}, labels)
	if len(got) != 2 || got[0] != "values-prod.yaml" || got[1] != "helm.values in apps/argo-cd.yaml" {
		t.Fatalf("relabel = %v", got)
	}
	if got := inlineValuesLabel(filepath.Join(os.TempDir(), "sp-tf-argo-1", "argocd-new.yaml")); got != "inline helm.values" {
		t.Fatalf("a synthesized Application has no path to show, got %q", got)
	}
	if relabel(nil, labels) != nil {
		t.Fatalf("no values files must stay empty")
	}
}

func TestChartVersionFromTFMissing(t *testing.T) {
	if _, err := config.FirstCapture(`variable "replicas" { default = "1" }`, `variable "chart_version"[^}]*default\s*=\s*"([^"]+)"`); err == nil {
		t.Fatal("expected no match")
	}
}
