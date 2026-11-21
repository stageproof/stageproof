package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestFactsExportRequiresFlags(t *testing.T) {
	if code := runFacts(context.Background(), nil); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if code := runFacts(context.Background(), []string{"export"}); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestClusterFactsSetTheVersionWhenTheFlagIsUnset(t *testing.T) {
	dir := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  namespace: default\ndata:\n  note: one\n"
	writeFile(t, "before.yaml", manifest)
	writeFile(t, "after.yaml", manifest)
	writeFile(t, ".stageproof.yaml", "currentKubernetes: \"1.99\"\n")
	writeFile(t, "facts.json", `{
  "schema_version": 1,
  "server_version": "1.31.4",
  "nodes": [{"name": "a", "architecture": "amd64", "operating_system": "linux"}]
}
`)

	code := runCheckCtx(context.Background(), []string{
		"--before", "before.yaml", "--after", "after.yaml",
		"--api-server", "off", "--cluster-facts", "facts.json",
		"--format", "json", "--out", "report.json",
	})
	rep := readReport(t, "report.json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if rep.TargetK8s != "1.31" {
		t.Fatalf("facts file should set the version, got %s", rep.TargetK8s)
	}

	code = runCheckCtx(context.Background(), []string{
		"--before", "before.yaml", "--after", "after.yaml",
		"--api-server", "off", "--cluster-facts", "facts.json",
		"--current-k8s", "1.32",
		"--format", "json", "--out", "flag.json",
	})
	flagged := readReport(t, "flag.json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if flagged.TargetK8s != "1.32" {
		t.Fatalf("flag should win, got %s", flagged.TargetK8s)
	}

	code = runCheckCtx(context.Background(), []string{
		"--before", "before.yaml", "--after", "after.yaml",
		"--api-server", "off",
		"--format", "json", "--out", "file.json",
	})
	fromFile := readReport(t, "file.json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if fromFile.TargetK8s != "1.99" {
		t.Fatalf("without the flag the file stands, got %s", fromFile.TargetK8s)
	}
}

func TestCheckPRPassesClusterFactsFromTheOperator(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil || exec.Command("git", "version").Run() != nil {
		t.Skip("helm or git is not installed")
	}
	repo := t.TempDir()
	factsPath := filepath.Join(t.TempDir(), "facts.json")
	if err := os.WriteFile(factsPath, []byte(`{"schema_version":1,"server_version":"1.31.4"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	chart(t, "charts/api", "api", "1.0.0", "config.logLevel")
	writeFile(t, ".stageproof.yaml", `currentKubernetes: "1.99"
charts:
  - name: api
    path: charts/api
`)
	git(t, "init", "-q", "-b", "main")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "baseline")
	git(t, "checkout", "-q", "-b", "change")
	chart(t, "charts/api", "api", "2.0.0", "config.logLevel")
	git(t, "add", "-A")
	git(t, "commit", "-q", "-m", "bump")

	code := runCheckPR([]string{
		"--current-git", "main", "--api-server", "off", "--offline",
		"--cluster-facts", factsPath, "--out", "report.json",
	})
	raw, err := os.ReadFile("report.json")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	var combined report.Combined
	if err := json.Unmarshal(raw, &combined); err != nil {
		t.Fatal(err)
	}
	if len(combined.Reports) != 1 || combined.Reports[0].TargetK8s != "1.31" {
		t.Fatalf("cluster facts were not passed through: %s", raw)
	}
}

func readReport(t *testing.T, path string) verdict.Report {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep verdict.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, raw)
	}
	return rep
}
