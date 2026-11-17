package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// chart writes a one-Deployment chart. logKey is the values path the template
// reads for the log level, so that a rename between two versions can be staged.
func chart(t *testing.T, dir, name, version, logKey string) {
	t.Helper()
	parts := strings.Split(logKey, ".")
	writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: "+name+"\nversion: "+version+"\n")
	writeFile(t, filepath.Join(dir, "values.yaml"), "replicas: 1\n"+parts[0]+":\n  "+parts[1]+": info\n")
	writeFile(t, filepath.Join(dir, "templates", "deployment.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}-`+name+`
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: `+name+`
  template:
    metadata:
      labels:
        app: `+name+`
    spec:
      containers:
        - name: app
          image: nginx:1.27-alpine
          env:
            - name: LOG_LEVEL
              value: "{{ .Values.`+logKey+` }}"
`)
}

// A pull request that changes two charts must produce one comment and one
// report that cover both. The Kubernetes version and the values files of a
// chart come from .stageproof.yaml when the command line does not give them.
func TestCheckPRCombinesChartsAndReadsConfig(t *testing.T) {
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

	chart(t, "charts/api", "api", "1.0.0", "config.logLevel")
	chart(t, "charts/web", "web", "1.0.0", "config.logLevel")
	writeFile(t, "charts/api/values-prod.yaml", "replicas: 3\nconfig:\n  logLevel: warn\n")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.31"
charts:
  - name: api
    path: charts/api
    valuesFiles:
      - charts/api/values-prod.yaml
      - charts/api/values.yaml
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")

	// The branch renames a values key in api and bumps the version of web.
	git(t, "checkout", "-q", "-b", "change")
	chart(t, "charts/api", "api", "2.0.0", "logging.level")
	chart(t, "charts/web", "web", "1.0.1", "config.logLevel")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "change both charts")

	code := runCheckPR([]string{"--current-git", "main", "--comment-file", "comment.md", "--out", "report.json"})
	if code != 1 {
		t.Fatalf("the renamed key in values-prod.yaml must block the pull request, got exit %d", code)
	}

	raw, err := os.ReadFile("report.json")
	if err != nil {
		t.Fatalf("the report must be written when several charts changed: %v", err)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	if combined.Verdict != verdict.Blocked || combined.ExitCode != 1 {
		t.Fatalf("combined result = %s (exit %d)", combined.Verdict, combined.ExitCode)
	}
	if len(combined.Reports) != 3 {
		t.Fatalf("expected api with two values files and web with one, got %d reports", len(combined.Reports))
	}
	results := map[string]verdict.State{}
	for _, r := range combined.Reports {
		if r.TargetK8s != "1.31" {
			t.Fatalf("currentKubernetes from .stageproof.yaml was not used: %q", r.TargetK8s)
		}
		results[r.HeadRef+" "+strings.Join(r.ValuesFiles, ",")] = r.Verdict
	}
	want := map[string]verdict.State{
		"charts/api charts/api/values-prod.yaml": verdict.Blocked,
		"charts/api charts/api/values.yaml":      verdict.Passed,
		"charts/web charts/web/values.yaml":      verdict.Passed,
	}
	for key, state := range want {
		if results[key] != state {
			t.Fatalf("%s = %q, want %s (all: %v)", key, results[key], state, results)
		}
	}

	comment, err := os.ReadFile("comment.md")
	if err != nil {
		t.Fatalf("the comment must be written when several charts changed: %v", err)
	}
	if !strings.HasPrefix(string(comment), "## Stageproof: `BLOCKED` (exit `1`) across 3 checks") {
		t.Fatalf("comment must start with the combined result:\n%s", comment)
	}
	if !strings.Contains(string(comment), "helm.values.silently_dropped") {
		t.Fatalf("comment must contain the finding:\n%s", comment)
	}

	// A flag on the command line wins over the file.
	_ = os.Remove("report.json")
	runCheckPR([]string{"--current-git", "main", "--current-k8s", "1.35", "--out", "report.json"})
	raw, err = os.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	combined = report.Combined{}
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	for _, r := range combined.Reports {
		if r.TargetK8s != "1.35" {
			t.Fatalf("--current-k8s must override the file, got %q", r.TargetK8s)
		}
		if r.Verdict == verdict.Passed && r.Checked != verdict.CheckedFiles {
			t.Fatalf("a file check must say what it checked, got %q", r.Checked)
		}
	}
}

func TestRequireRehearsalWithoutLiveIsNotChecked(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
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

	chart(t, "old", "app", "1.0.0", "config.logLevel")
	chart(t, "new", "app", "1.1.0", "config.logLevel")

	if code := runCheck([]string{"--current-chart", "old", "--new-chart", "new"}); code != 0 {
		t.Fatalf("file checks on a clean upgrade must exit 0, got %d", code)
	}
	if code := runCheck([]string{"--current-chart", "old", "--new-chart", "new", "--require-rehearsal"}); code != 2 {
		t.Fatalf("--require-rehearsal without --live must exit 2, got %d", code)
	}
}
