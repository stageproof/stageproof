package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestUpgradeModeFallbackWarnsAndKeepsChecking(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := writeTestChart(t, "1.0.0", "password: \"\"\n", `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  v: "1"
`)
	head := writeTestChart(t, "1.1.0", "password: \"\"\n", `{{- if .Release.IsUpgrade }}
{{- fail (printf "existing password %s required on upgrade" .Values.password) }}
{{- end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  v: "2"
`)
	vals := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(vals, []byte("password: supersecretvalue\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep := Run(context.Background(), Input{
		BaseDir:     base,
		HeadDir:     head,
		BaseValues:  []string{vals},
		HeadValues:  []string{vals},
		KubeVersion: "1.35",
		APIServer:   "off",
		ValuesFull:  true,
		Started:     time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict = %s, want PASSED; findings = %+v", rep.Verdict, rep.Findings)
	}
	var found bool
	for _, f := range rep.Findings {
		if f.RuleID == "helm.template.upgrade_fallback" {
			found = true
			if f.Severity != verdict.SeverityWarning {
				t.Fatalf("fallback severity = %s, want warning", f.Severity)
			}
			if strings.Contains(f.Detail, "supersecretvalue") || strings.Contains(f.Message, "supersecretvalue") {
				t.Fatalf("secret leaked in fallback finding: %+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("expected helm.template.upgrade_fallback warning, got %+v", rep.Findings)
	}
}

func TestUpgradePasswordPlaceholderRendersUpgradeMode(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	template := `{{- if and .Release.IsUpgrade (not .Values.auth.password) }}
{{- fail "'auth.password' must not be empty, please add '--set auth.password=$PASSWORD' to the command." }}
{{- end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  v: {{ .Chart.Version | quote }}
`
	base := writeTestChart(t, "1.0.0", "auth:\n  password: \"\"\n", template)
	head := writeTestChart(t, "1.1.0", "auth:\n  password: \"\"\n", template)
	rep := Run(context.Background(), Input{
		BaseDir:     base,
		HeadDir:     head,
		KubeVersion: "1.35",
		APIServer:   "off",
		Started:     time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict = %s, want PASSED; findings = %+v checks = %+v", rep.Verdict, rep.Findings, rep.Checks)
	}
	for _, f := range rep.Findings {
		if f.RuleID == "helm.template.upgrade_fallback" {
			t.Fatalf("upgrade password should render in upgrade mode, got %+v", f)
		}
	}
	if !checkMentions(rep, "chart defaults", "auth.password") {
		t.Fatalf("placeholder key missing from checks: %+v", rep.Checks)
	}
}

func TestInstallOnlyValuesKeyNotFalselyDroppedOnUpgrade(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := writeTestChart(t, "1.0.0", "initTag: \"1\"\nappTag: \"1\"\n", `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  tag: {{ .Values.appTag | quote }}
{{- if .Release.IsInstall }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: init
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: init
          image: busybox:{{ .Values.initTag }}
{{- end }}
`)
	headKept := writeTestChart(t, "1.1.0", "initTag: \"1\"\nappTag: \"2\"\n", `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  tag: {{ .Values.appTag | quote }}
{{- if .Release.IsInstall }}
---
apiVersion: batch/v1
kind: Job
metadata:
  name: init
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: init
          image: busybox:{{ .Values.initTag }}
{{- end }}
`)
	headDropped := writeTestChart(t, "1.1.0", "appTag: \"2\"\n", `apiVersion: v1
kind: ConfigMap
metadata:
  name: app
data:
  tag: {{ .Values.appTag | quote }}
`)
	vals := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(vals, []byte("initTag: custom\nappTag: prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	keptRep := Run(context.Background(), Input{
		BaseDir:     base,
		HeadDir:     headKept,
		BaseValues:  []string{vals},
		HeadValues:  []string{vals},
		KubeVersion: "1.35",
		APIServer:   "off",
		ValuesFull:  true,
		Started:     time.Now(),
	})
	if keptRep.Verdict != verdict.Passed {
		t.Fatalf("install-only key kept in new chart must PASS, got %s: %+v", keptRep.Verdict, keptRep.Findings)
	}

	droppedRep := Run(context.Background(), Input{
		BaseDir:     base,
		HeadDir:     headDropped,
		BaseValues:  []string{vals},
		HeadValues:  []string{vals},
		KubeVersion: "1.35",
		APIServer:   "off",
		ValuesFull:  true,
		Started:     time.Now(),
	})
	if droppedRep.Verdict != verdict.Blocked {
		t.Fatalf("genuinely dropped install-only key must BLOCK, got %s: %+v", droppedRep.Verdict, droppedRep.Findings)
	}
	if !hasRule(droppedRep.Findings, "helm.values.silently_dropped") {
		t.Fatalf("expected helm.values.silently_dropped, got %+v", droppedRep.Findings)
	}
}

func writeTestChart(t *testing.T, version, valuesYAML, templateYAML string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: "+version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(valuesYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "all.yaml"), []byte(templateYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
