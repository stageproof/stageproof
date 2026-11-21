package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetDropBlocks(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := t.TempDir()
	head := t.TempDir()
	writeSetChart(t, base, `{{ .Values.config.logLevel | quote }}`)
	writeSetChart(t, head, `"fixed"`)
	out := filepath.Join(t.TempDir(), "report.json")
	code := runCheck([]string{
		"--current-chart", base,
		"--new-chart", head,
		"--set", "config.logLevel=warn",
		"--api-server", "off",
		"--format", "json",
		"--out", out,
	})
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("exit %d: %v", code, err)
	}
	if code != 1 || !strings.Contains(string(raw), "helm.values.silently_dropped") || !strings.Contains(string(raw), `"proof": "render"`) {
		t.Fatalf("exit %d\n%s", code, raw)
	}
}

func TestCheckPRMissingValuesFileIsNotChecked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
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

	writeSetChart(t, "charts/web", `"ok"`)
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
charts:
  - name: web
    path: charts/web
    valuesFiles:
      - charts/web/values-prod.yaml
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	writeFile(t, "charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.2.0\n")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "bump")

	code, buf := runCheckPRStderr([]string{"--current-git", "main", "--api-server", "off"})
	if code != 2 || !strings.Contains(buf, "values-prod.yaml") {
		t.Fatalf("exit %d\n%s", code, buf)
	}
}

func TestCheckPRPolicyBlockBeatsNotChecked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
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

	writeSetChart(t, "charts/web", `"ok"`)
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
policy:
  config.changed_in_pr: block
charts:
  - name: web
    path: charts/web
    valuesFiles:
      - charts/web/values-prod.yaml
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
policy:
  config.changed_in_pr: block
charts:
  - name: web
    path: charts/web
    valuesFiles:
      - charts/web/values-prod.yaml
# changed
`)
	writeFile(t, "charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.2.0\n")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "change settings")

	code, buf := runCheckPRStderr([]string{"--current-git", "main", "--api-server", "off"})
	if code != 1 {
		t.Fatalf("policy block must exit 1, got %d\n%s", code, buf)
	}
}

func runCheckPRStderr(args []string) (int, string) {
	r, w, err := os.Pipe()
	if err != nil {
		return 2, err.Error()
	}
	old := os.Stderr
	os.Stderr = w
	code := runCheckPR(args)
	_ = w.Close()
	os.Stderr = old
	buf, _ := io.ReadAll(r)
	return code, string(buf)
}

func writeSetChart(t *testing.T, dir, image string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(dir, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(dir, "templates", "cm.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  level: "+image+"\n")
}
