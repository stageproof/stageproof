package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayCasesFile(t *testing.T) {
	cases, err := loadReplayCases(filepath.Join("..", "..", "corpus", "replay", "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) < 50 {
		t.Fatalf("got %d cases", len(cases))
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "corpus", "replay", "cases.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "BEGIN ") || strings.Contains(string(raw), "values:") {
		t.Fatal("cases.jsonl contains more than the case fields")
	}
}

func TestReplayLineRejectsExtraFields(t *testing.T) {
	_, err := parseReplayLine(`{"repo":"example/tools","sha":"0123456789abcdef0123456789abcdef01234567","path":"apps/web.yaml","from":"1.2.3","to":"1.2.4","values":"nope"}`)
	if err == nil {
		t.Fatal("extra field was accepted")
	}
	_, err = parseReplayLine(`{"repo":"../tools","sha":"0123456789abcdef0123456789abcdef01234567","path":"apps/web.yaml","from":"1.2.3","to":"1.2.4"}`)
	if err == nil {
		t.Fatal("repo escape was accepted")
	}
}

func TestReplaySummaryOmitsCaseNames(t *testing.T) {
	text := formatReplaySummary(summarizeReplay([]caseResult{
		{ID: "replay-001", Kind: "replay", Verdict: "PASSED", OK: true, Seconds: 1},
		{ID: "replay-002", Kind: "replay", Verdict: "BLOCKED", OK: true, Seconds: 3},
		{ID: "replay-003", Kind: "replay", Note: "fetch failed", Seconds: 0.2},
	}))
	if !strings.Contains(text, "replay 2 checked / 3 cases") || !strings.Contains(text, "PASSED 1") || !strings.Contains(text, "BLOCKED 1") || !strings.Contains(text, "errors 1") {
		t.Fatalf("summary:\n%s", text)
	}
	if strings.Contains(text, "github.com") || strings.Contains(text, "example/tools") {
		t.Fatal("summary names a repository")
	}
}

func TestCheckReplayExitTwoWithoutReport(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-stageproof")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	verdict, rules, err := checkReplay(bin, t.TempDir(), "apps/web.yaml")
	if err != nil || verdict != "NOT CHECKED" || len(rules) != 0 {
		t.Fatalf("verdict %q rules %v err %v", verdict, rules, err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := checkReplay(bin, t.TempDir(), "apps/web.yaml"); err == nil {
		t.Fatal("exit 3 was accepted")
	}
}

func TestCheckReplayOnTwoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	repo := t.TempDir()
	gitReplay(t, repo, "init", "-q", "-b", "main")
	gitReplay(t, repo, "remote", "add", "origin", "https://example.test/stageproof.git")
	writeReplayFile(t, filepath.Join(repo, "charts/web/Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeReplayFile(t, filepath.Join(repo, "charts/web/values.yaml"), "tag: \"1.27\"\n")
	writeReplayFile(t, filepath.Join(repo, "charts/web/templates/deploy.yaml"), `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
spec:
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
          image: nginx:{{ .Values.tag }}
`)
	writeReplayFile(t, filepath.Join(repo, "sources/git.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: charts\nspec:\n  url: https://example.test/stageproof.git\n")
	writeReplayFile(t, filepath.Join(repo, "apps/web.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
spec:
  chart:
    spec:
      chart: charts/web
      sourceRef:
        kind: GitRepository
        name: charts
  values:
    tag: "1.27"
`)
	gitReplay(t, repo, "add", "-A")
	gitReplay(t, repo, "commit", "-q", "-m", "base")
	writeReplayFile(t, filepath.Join(repo, "apps/web.yaml"), strings.Replace(mustReadReplay(t, filepath.Join(repo, "apps/web.yaml")), "1.27", "1.28", 1))
	gitReplay(t, repo, "add", "-A")
	gitReplay(t, repo, "commit", "-q", "-m", "bump")

	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := buildCLI(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(bin)) })
	verdict, rules, err := checkReplay(bin, repo, "apps/web.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if verdict != "PASSED" && verdict != "BLOCKED" {
		t.Fatalf("verdict %s rules %v", verdict, rules)
	}
	found := false
	for _, rule := range rules {
		if rule == "helm.image.changed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("image change was not recorded: %v", rules)
	}
}

func gitReplay(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeReplayFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadReplay(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
