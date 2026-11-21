package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestCheckPRReadsRefValuesFromThisRepo(t *testing.T) {
	for _, bin := range []string{"git", "helm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	repo := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	writeFile(t, "charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, "charts/web/values.yaml", "replicas: 1\n")
	writeFile(t, "charts/web/templates/deploy.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: web
  template:
    metadata:
      labels:
        app: web
    spec:
      containers:
        - name: app
          image: nginx:{{ .Values.tag | required "tag" }}
`)
	writeFile(t, "values/prod.yaml", "tag: \"1.27\"\n")
	writeFile(t, "apps/web.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: web
spec:
  sources:
    - repoURL: https://example.test/stageproof.git
      path: charts/web
      helm:
        valueFiles:
          - $values/values/prod.yaml
    - repoURL: https://example.test/stageproof
      ref: values
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "remote", "add", "origin", "https://example.test/stageproof.git")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	writeFile(t, "values/prod.yaml", "tag: \"1.28\"\n")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "bump the tag")

	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", "report.json"})
	raw, readErr := os.ReadFile("report.json")
	if readErr != nil {
		t.Fatalf("exit %d, no report: %v", code, readErr)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.Reports) != 1 {
		t.Fatalf("reports = %d", len(combined.Reports))
	}
	rep := combined.Reports[0]
	if rep.Adapter != "argocd" || rep.Verdict != verdict.Passed {
		t.Fatalf("report %s adapter %s", rep.Verdict, rep.Adapter)
	}
	blob := string(raw)
	if !strings.Contains(blob, "values/prod.yaml") {
		t.Fatalf("the $ref values file was not used:\n%s", blob)
	}
	if !strings.Contains(blob, "helm.image.changed") {
		t.Fatalf("the old and new revisions were not both read:\n%s", blob)
	}
}

func TestCheckPRApplicationSetAndForeignRepoAreNotChecked(t *testing.T) {
	for _, bin := range []string{"git", "helm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	repo := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	writeFile(t, "apps/set.yaml", `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: demo
spec: {}
`)
	writeFile(t, "apps/foreign.yaml", `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: foreign
spec:
  source:
    repoURL: https://127.0.0.1:1/other.git
    path: charts/web
    targetRevision: main
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "remote", "add", "origin", "https://example.test/stageproof.git")
	git(t, "commit", "-q", "--allow-empty", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "add applications")

	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", "report.json"})
	raw, readErr := os.ReadFile("report.json")
	if readErr != nil {
		t.Fatalf("exit %d, no report: %v", code, readErr)
	}
	if code != 2 {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	blob := string(raw)
	if !strings.Contains(blob, "ApplicationSet") || !strings.Contains(blob, "does not clone") {
		t.Fatalf("reasons missing:\n%s", blob)
	}
	if strings.Contains(blob, "stageproof-argocd-") {
		t.Fatalf("a temp chart was rendered for a refused source:\n%s", blob)
	}
}
