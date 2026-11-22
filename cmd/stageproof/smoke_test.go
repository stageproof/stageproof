package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveOffDoesNotRunSmoke(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	repo := t.TempDir()
	chdir(t, repo)
	writeSetChart(t, "charts/web", `"ok"`)
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
smoke:
  - name: marker
    run: touch smoke-marker
`)
	code := runCheck([]string{
		"--current-chart", "charts/web",
		"--new-chart", "charts/web",
		"--api-server", "off",
		"--format", "json",
		"--out", filepath.Join(repo, "report.json"),
	})
	raw, err := os.ReadFile(filepath.Join(repo, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "smoke-marker")); !os.IsNotExist(statErr) {
		t.Fatal("smoke ran without --live")
	}
	if code != 0 || strings.Contains(string(raw), "smoke:") || strings.Contains(string(raw), "live.smoke_failed") {
		t.Fatalf("exit %d\n%s", code, raw)
	}
}

func TestCheckPRHeadSmokeDoesNotRun(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	repo := t.TempDir()
	chdir(t, repo)
	writeSetChart(t, "charts/web", `"ok"`)
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
smoke:
  - name: base
    run: touch base-marker
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.33"
smoke:
  - name: head
    run: touch head-marker
`)
	writeFile(t, "charts/web/Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.2.0\n")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "add smoke")

	cfg, changed, err := configFromBase("main", "HEAD")
	if err != nil || !changed || len(cfg.Smoke) != 1 || cfg.Smoke[0].Name != "base" {
		t.Fatalf("base smoke = %+v changed=%v err=%v", cfg.Smoke, changed, err)
	}
	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--format", "json", "--out", filepath.Join(repo, "report.json")})
	raw, readErr := os.ReadFile(filepath.Join(repo, "report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if _, statErr := os.Stat(filepath.Join(repo, "head-marker")); !os.IsNotExist(statErr) {
		t.Fatal("the pull request smoke command ran")
	}
	if _, statErr := os.Stat(filepath.Join(repo, "base-marker")); !os.IsNotExist(statErr) {
		t.Fatal("smoke ran without --live")
	}
	if code != 0 || !strings.Contains(string(raw), "config.changed_in_pr") || strings.Contains(string(raw), "live.smoke_failed") {
		t.Fatalf("exit %d\n%s", code, raw)
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
