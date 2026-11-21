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

func TestCheckPRIgnoreOnHeadDoesNotBypass(t *testing.T) {
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

	chart(t, "charts/api", "api", "1.0.0", "config.logLevel")
	writeFile(t, "charts/api/values-prod.yaml", "replicas: 1\nconfig:\n  logLevel: warn\n")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.31"
charts:
  - name: api
    path: charts/api
    valuesFiles:
      - charts/api/values-prod.yaml
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	chart(t, "charts/api", "api", "2.0.0", "logging.level")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.99"
ignores:
  - rule: helm.values.silently_dropped
    resource: "*"
    reason: "we renamed it"
    expires: "2099-01-01"
charts:
  - name: api
    path: charts/api
    valuesFiles:
      - charts/api/values-prod.yaml
`)
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "hide the finding")

	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", "report.json"})
	raw, err := os.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 {
		t.Fatalf("an ignore added on the branch must not bypass the blocker, exit %d\n%s", code, raw)
	}
	if strings.Contains(string(raw), "we renamed it") || strings.Contains(string(raw), "1.99") {
		t.Fatalf("the branch file was used:\n%s", raw)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.Reports) != 1 {
		t.Fatalf("reports: %d", len(combined.Reports))
	}
	rep := combined.Reports[0]
	if rep.TargetK8s != "1.31" {
		t.Fatalf("kubernetes version came from the branch file: %s", rep.TargetK8s)
	}
	var dropped, changed bool
	for _, f := range rep.Findings {
		if f.RuleID == "helm.values.silently_dropped" {
			dropped = true
			if f.Ignored {
				t.Fatalf("the branch ignore matched: %+v", f)
			}
		}
		if f.RuleID == "config.changed_in_pr" {
			changed = true
		}
	}
	if !dropped || !changed {
		t.Fatalf("findings: %+v", rep.Findings)
	}
}

func TestCheckPRSettingsChangeWarnsWhenNoChartChanged(t *testing.T) {
	if exec.Command("git", "version").Run() != nil {
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
	writeFile(t, ".stageproof.yaml", "currentKubernetes: \"1.31\"\n")
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	writeFile(t, ".stageproof.yaml", "currentKubernetes: \"1.32\"\nignores:\n  - rule: helm.values.silently_dropped\n    resource: \"*\"\n    reason: \"not on the base\"\n    expires: \"2099-01-01\"\n")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "edit settings")

	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", "report.json"})
	raw, err := os.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || !strings.Contains(string(raw), "config.changed_in_pr") || strings.Contains(string(raw), "not on the base") {
		t.Fatalf("exit %d\n%s", code, raw)
	}
}

func TestCheckPRIgnoreOnBaseIsHonored(t *testing.T) {
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

	chart(t, "charts/api", "api", "1.0.0", "config.logLevel")
	writeFile(t, "charts/api/values-prod.yaml", "replicas: 1\nconfig:\n  logLevel: warn\n")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.31"
ignores:
  - rule: helm.values.silently_dropped
    resource: "*"
    reason: "The chart renamed this key."
    expires: "2099-01-01"
charts:
  - name: api
    path: charts/api
    valuesFiles:
      - charts/api/values-prod.yaml
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	chart(t, "charts/api", "api", "2.0.0", "logging.level")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "rename the key")

	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", "report.json"})
	raw, err := os.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.Reports) != 1 {
		t.Fatalf("exit %d reports %d\n%s", code, len(combined.Reports), raw)
	}
	rep := combined.Reports[0]
	var dropped *verdict.Finding
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.RuleID == "config.changed_in_pr" {
			t.Fatalf("the settings file did not change:\n%s", raw)
		}
		if f.RuleID == "helm.values.silently_dropped" {
			dropped = f
		}
	}
	if dropped == nil || !dropped.Ignored || dropped.IgnoreExpires != "2099-01-01" || !strings.Contains(dropped.IgnoreReason, "renamed") {
		t.Fatalf("finding: %+v\n%s", dropped, raw)
	}
	if code != 0 || rep.Verdict != verdict.Passed {
		t.Fatalf("base ignore should leave the result passed, exit %d %s\n%s", code, rep.Verdict, raw)
	}
}
