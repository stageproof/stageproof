package helm

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestTemplateGuardsRemovedAPI(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "templates")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("templates/psp-inline.yaml", `{{- if and .Values.enabled (.Capabilities.APIVersions.Has "policy/v1beta1") }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
{{- end }}
`)
	write("templates/psp-kind.yaml", `{{- if .Capabilities.APIVersions.Has "policy/v1beta1/PodSecurityPolicy" }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
{{- end }}
`)
	write("templates/psp-has-func.yaml", `{{- if has "policy/v1beta1" .Capabilities.APIVersions }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
{{- end }}
`)
	write("templates/_helpers.tpl", `{{- define "demo.psp.supported" -}}
{{- if .Capabilities.APIVersions.Has "policy/v1beta1" -}}true{{- end -}}
{{- end -}}
`)
	write("templates/psp-helper.yaml", `{{- if include "demo.psp.supported" . }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
{{- end }}
`)
	write("templates/psp-unconditional.yaml", `apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
`)
	write("templates/pdb-else-fallback.yaml", `{{- if .Capabilities.APIVersions.Has "policy/v1" }}
apiVersion: policy/v1
{{- else }}
apiVersion: policy/v1beta1
{{- end }}
kind: PodDisruptionBudget
metadata:
  name: demo
`)
	write("templates/psp-negated.yaml", `{{- if not (.Capabilities.APIVersions.Has "policy/v1beta1") }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
metadata:
  name: demo
{{- end }}
`)
	write("charts/sub/templates/psp.yaml", `{{- if .Capabilities.APIVersions.Has "policy/v1beta1" }}
apiVersion: policy/v1beta1
kind: PodSecurityPolicy
{{- end }}
`)

	cases := []struct {
		name       string
		source     string
		apiVersion string
		kind       string
		want       bool
	}{
		{"inline group/version", "demo/templates/psp-inline.yaml", "policy/v1beta1", "PodSecurityPolicy", true},
		{"inline group/version/kind", "demo/templates/psp-kind.yaml", "policy/v1beta1", "PodSecurityPolicy", true},
		{"sprig has function", "demo/templates/psp-has-func.yaml", "policy/v1beta1", "PodSecurityPolicy", true},
		{"helper in _helpers.tpl", "demo/templates/psp-helper.yaml", "policy/v1beta1", "PodSecurityPolicy", true},
		{"unpacked subchart", "demo/charts/sub/templates/psp.yaml", "policy/v1beta1", "PodSecurityPolicy", true},
		{"unconditional removed API", "demo/templates/psp-unconditional.yaml", "policy/v1beta1", "PodSecurityPolicy", false},
		{"else fallback checking v1 not v1beta1", "demo/templates/pdb-else-fallback.yaml", "policy/v1beta1", "PodDisruptionBudget", false},
		{"negated check", "demo/templates/psp-negated.yaml", "policy/v1beta1", "PodSecurityPolicy", false},
		{"path traversal rejected", "demo/../../etc/passwd", "policy/v1beta1", "PodSecurityPolicy", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TemplateGuardsRemovedAPI(dir, tc.source, tc.apiVersion, tc.kind); got != tc.want {
				t.Fatalf("TemplateGuardsRemovedAPI(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}

func TestTemplateGuardsRemovedAPIPackagedSubchart(t *testing.T) {
	dir := t.TempDir()
	chartsDir := filepath.Join(dir, "charts")
	if err := os.MkdirAll(chartsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(chartsDir, "pkgsub-1.0.0.tgz")
	f, err := os.Create(tgz)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	content := []byte("{{- if .Capabilities.APIVersions.Has \"policy/v1beta1\" }}\napiVersion: policy/v1beta1\nkind: PodSecurityPolicy\n{{- end }}\n")
	if err := tw.WriteHeader(&tar.Header{Name: "pkgsub/templates/psp.yaml", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if !TemplateGuardsRemovedAPI(dir, "root/charts/pkgsub/templates/psp.yaml", "policy/v1beta1", "PodSecurityPolicy") {
		t.Fatal("expected packaged subchart capability guard to be detected")
	}
}

func TestTemplateUsesReleaseRevision(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("templates/seed-direct.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Release.Name }}-seed-{{ .Release.Revision }}
`)
	write("templates/_helpers.tpl", `{{- define "demo.seedName" -}}
{{ .Release.Name }}-seed-{{ .Release.Revision }}
{{- end -}}
`)
	write("templates/seed-helper.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ include "demo.seedName" . }}
`)
	write("templates/static-with-comment.yaml", `{{/* .Release.Revision is mentioned only in a comment */}}
apiVersion: batch/v1
kind: Job
metadata:
  name: fixed-job
`)
	write("templates/static-with-annotation.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  annotations:
    rev: "{{ .Release.Revision }}"
`)
	write("templates/dynamic-no-revision.yaml", `apiVersion: batch/v1
kind: Job
metadata:
  name: {{ .Release.Name }}-init
`)

	cases := []struct {
		name   string
		source string
		want   bool
	}{
		{"direct revision in name", "demo/templates/seed-direct.yaml", true},
		{"helper revision in name", "demo/templates/seed-helper.yaml", true},
		{"comment only with static name", "demo/templates/static-with-comment.yaml", false},
		{"annotation only with static name", "demo/templates/static-with-annotation.yaml", false},
		{"dynamic name without revision", "demo/templates/dynamic-no-revision.yaml", false},
		{"path traversal rejected", "demo/../../etc/passwd", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := TemplateUsesReleaseRevision(dir, tc.source); got != tc.want {
				t.Fatalf("TemplateUsesReleaseRevision(%q) = %v, want %v", tc.source, got, tc.want)
			}
		})
	}
}
