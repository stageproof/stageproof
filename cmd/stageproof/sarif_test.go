package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFormatSARIFKeepsTheExitCode(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := moduleRoot(t)
	textCode, textOut := captureCheck(t, root, []string{
		"--current-chart", "testdata/dropped/base",
		"--new-chart", "testdata/dropped/head",
		"-f", "testdata/dropped/values.yaml",
		"--api-server", "off",
		"--format", "text",
	})
	sarifCode, sarifOut := captureCheck(t, root, []string{
		"--current-chart", "testdata/dropped/base",
		"--new-chart", "testdata/dropped/head",
		"-f", "testdata/dropped/values.yaml",
		"--api-server", "off",
		"--format", "sarif",
	})
	if textCode != 1 || sarifCode != 1 {
		t.Fatalf("text exit %d sarif exit %d\ntext:\n%s\nsarif:\n%s", textCode, sarifCode, textOut, sarifOut)
	}
	if !strings.Contains(sarifOut, `"version": "2.1.0"`) || !strings.Contains(sarifOut, `"ruleId": "helm.values.silently_dropped"`) {
		t.Fatalf("sarif:\n%s", sarifOut)
	}
	if strings.Contains(sarifOut, "stageproof  BLOCKED") {
		t.Fatalf("sarif stdout included the terminal report:\n%s", sarifOut)
	}
	if strings.Count(sarifOut, `"version": "2.1.0"`) != 1 {
		t.Fatalf("one check must print one SARIF log:\n%s", sarifOut)
	}
}

func TestCheckPRFormatSARIFIsOneLog(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil || exec.Command("git", "version").Run() != nil {
		t.Skip("helm or git is not installed")
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
	chart(t, "charts/web", "web", "1.0.0", "config.logLevel")
	writeFile(t, ".stageproof.yaml", `charts:
  - name: web
    path: charts/web
    valuesFiles:
      - charts/web/values-prod.yaml
      - charts/web/values.yaml
`)
	writeFile(t, "charts/web/values-prod.yaml", "config:\n  logLevel: warn\n")
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	chart(t, "charts/web", "web", "2.0.0", "config.logLevel")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "bump")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--format", "sarif"})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Count(out, `"version": "2.1.0"`) != 1 {
		t.Fatalf("a pull request must print one SARIF log:\n%s", out)
	}
	if !strings.Contains(out, `"ruleId": "helm.release.version_span"`) {
		t.Fatalf("the chart change must be in the log:\n%s", out)
	}
}

func captureCheck(t *testing.T, root string, args []string) (int, string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := runCheck(args)
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return code, buf.String()
}
