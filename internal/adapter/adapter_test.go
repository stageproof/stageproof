package adapter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverKeepsADeletedChartFile(t *testing.T) {
	root := t.TempDir()
	chart := filepath.Join(root, "charts", "api")
	write(t, filepath.Join(chart, "Chart.yaml"), "apiVersion: v2\nname: api\nversion: 1.0.0\n")
	gone := filepath.Join(chart, "templates", "pvc.yaml")
	write(t, gone, "apiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: data\n")
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	got := New(root).Discover(root, []string{"charts/api/templates/pvc.yaml"})
	if len(got) != 1 || got[0].Kind != KindHelm || got[0].Path != "charts/api" {
		t.Fatalf("deleted template = %+v", got)
	}

	outside := New(root).Discover(root, []string{"README.md"})
	if len(outside) != 0 {
		t.Fatalf("a deleted file outside a chart must not be a target: %+v", outside)
	}
}

func TestDiscoverSubchartBelongsToParent(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "charts", "api", "Chart.yaml"), "apiVersion: v2\nname: api\nversion: 1.0.0\n")
	write(t, filepath.Join(root, "charts", "api", "charts", "db", "Chart.yaml"), "apiVersion: v2\nname: db\nversion: 1.0.0\n")
	got := New(root).Discover(root, []string{"charts/api/charts/db/templates/svc.yaml"})
	if len(got) != 1 || got[0].Path != "charts/api" {
		t.Fatalf("subchart change = %+v", got)
	}
}

func TestDiscoverArgoAndApplication(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	chdir(t, root)
	write(t, filepath.Join(root, "apps", "demo.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  source:
    repoURL: https://charts.example.com
    chart: demo
    targetRevision: 1.2.3
`)
	write(t, filepath.Join(root, ".stageproof.yaml"), `applications:
  - name: billing
    repoURL: https://charts.example.com
    chart: billing
    versionFrom:
      file: version.txt
      regex: 'chart: (.*)'
    valuesFrom:
      - values.yaml
`)
	write(t, filepath.Join(root, "version.txt"), "chart: 1.2.4\n")
	write(t, filepath.Join(root, "values.yaml"), "replicas: 2\n")
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "base")
	write(t, filepath.Join(root, "version.txt"), "chart: 1.2.5\n")

	got := New(root).Discover(root, []string{"apps/demo.yaml", "version.txt"})
	if len(got) != 2 {
		t.Fatalf("targets = %+v", got)
	}
	if got[0].Kind != KindArgoCD || got[0].Path != filepath.Join(root, "apps", "demo.yaml") {
		t.Fatalf("argo target = %+v", got[0])
	}
	if got[1].Kind != KindApplication || got[1].Name != "billing" {
		t.Fatalf("application target = %+v", got[1])
	}

	resolved, err := New(root).Resolve(context.Background(), got[1], "main")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Cleanup != nil {
		t.Cleanup(resolved.Cleanup)
	}
	newRaw, err := os.ReadFile(resolved.ArgoFile)
	if err != nil {
		t.Fatal(err)
	}
	oldRaw, err := os.ReadFile(resolved.CurrentArgo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(newRaw), "targetRevision: 1.2.5") || !strings.Contains(string(oldRaw), "targetRevision: 1.2.4") {
		t.Fatalf("new:\n%s\nold:\n%s", newRaw, oldRaw)
	}
	if !strings.Contains(string(newRaw), "replicas: 2") {
		t.Fatalf("values were not copied:\n%s", newRaw)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
