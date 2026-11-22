package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestRemoteFlagsRejectIncompleteAndConflictingInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing chart", []string{"--repo", "https://charts.example", "--from", "0.1.0", "--to", "0.2.0"}, "--chart"},
		{"missing from", []string{"--repo", "https://charts.example", "--chart", "demo", "--to", "0.2.0"}, "--from"},
		{"missing to", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0"}, "--to"},
		{"chart path", []string{"--repo", "https://charts.example", "--chart", "./demo", "--from", "0.1.0", "--to", "0.2.0"}, "chart name"},
		{"bad scheme", []string{"--repo", "file:///tmp/charts", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0"}, "http://"},
		{"before", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0", "--before", "a", "--after", "b"}, "--before"},
		{"argocd", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0", "--argocd-file", "app.yaml"}, "--argocd-file"},
		{"flux", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0", "--flux-file", "hr.yaml"}, "--flux-file"},
		{"new chart", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0", "--new-chart", "./head"}, "--new-chart"},
		{"current chart", []string{"--repo", "https://charts.example", "--chart", "demo", "--from", "0.1.0", "--to", "0.2.0", "--current-chart", "./base"}, "--current-chart"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCheckCapture(t, tc.args)
			if code != 2 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr %q does not mention %s", stderr, tc.want)
			}
		})
	}
}

func TestChartAliasWithoutRepoStaysALocalDirectory(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := t.TempDir()
	old := filepath.Join(root, "old")
	next := filepath.Join(root, "new")
	chart(t, old, "web", "1.0.0", "config.logLevel")
	chart(t, next, "web", "1.1.0", "config.logLevel")
	chdir(t, root)

	code, _, stderr := runCheckCapture(t, []string{
		"--chart", next, "--api-server", "off", "--offline", "--format", "json", "--out", filepath.Join(root, "one.json"),
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	one := readVerdict(t, filepath.Join(root, "one.json"))
	if one.HeadRef != next {
		t.Fatalf("head_ref = %q, want the directory", one.HeadRef)
	}
	if strings.Contains(one.HeadRef, "@") {
		t.Fatalf("local --chart was read as a remote version: %s", one.HeadRef)
	}

	code, _, stderr = runCheckCapture(t, []string{
		"--from", old, "--to", next, "--api-server", "off", "--offline", "--format", "json", "--out", filepath.Join(root, "pair.json"),
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	pair := readVerdict(t, filepath.Join(root, "pair.json"))
	if pair.BaseRef != old || pair.HeadRef != next {
		t.Fatalf("refs = %s -> %s", pair.BaseRef, pair.HeadRef)
	}
}

func TestRemoteRepoCheck(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	pkg := t.TempDir()
	packageRemote(t, pkg, "0.1.0", configMapChart)
	packageRemote(t, pkg, "0.2.0", configMapChart)
	packageRemote(t, pkg, "0.3.0", removedAPIChart)
	srv := httptest.NewServer(http.FileServer(http.Dir(pkg)))
	t.Cleanup(srv.Close)
	index := exec.Command("helm", "repo", "index", pkg, "--url", srv.URL)
	if out, err := index.CombinedOutput(); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	work := t.TempDir()
	chdir(t, work)

	before := remoteTempNames()
	cleanOut := filepath.Join(work, "clean.json")
	code, _, stderr := runCheckCapture(t, remoteArgs(srv.URL, "0.1.0", "0.2.0", cleanOut))
	if code != 0 {
		t.Fatalf("clean exit %d\n%s", code, stderr)
	}
	clean := readVerdict(t, cleanOut)
	if clean.Verdict != verdict.Passed || clean.BaseRef != "demo@0.1.0" || clean.HeadRef != "demo@0.2.0" {
		t.Fatalf("clean report: %s %s -> %s", clean.Verdict, clean.BaseRef, clean.HeadRef)
	}
	assertNoRemoteTemp(t, cleanOut, before)

	brokenOut := filepath.Join(work, "broken.json")
	code, _, stderr = runCheckCapture(t, remoteArgs(srv.URL, "0.1.0", "0.3.0", brokenOut))
	if code != 1 {
		t.Fatalf("broken exit %d\n%s", code, stderr)
	}
	broken := readVerdict(t, brokenOut)
	if broken.Verdict != verdict.Blocked || broken.BaseRef != "demo@0.1.0" || broken.HeadRef != "demo@0.3.0" {
		t.Fatalf("broken report: %s %s -> %s", broken.Verdict, broken.BaseRef, broken.HeadRef)
	}
	assertNoRemoteTemp(t, brokenOut, before)

	missingOut := filepath.Join(work, "missing.json")
	code, _, stderr = runCheckCapture(t, remoteArgs(srv.URL, "0.1.0", "9.9.9", missingOut))
	if code != 2 {
		t.Fatalf("missing exit %d\n%s", code, stderr)
	}
	missing := readVerdict(t, missingOut)
	if missing.Verdict != verdict.NotChecked || missing.BaseRef != "demo@0.1.0" || missing.HeadRef != "demo@9.9.9" {
		t.Fatalf("missing report: %s %s -> %s", missing.Verdict, missing.BaseRef, missing.HeadRef)
	}
	if missing.ExitCode != 2 {
		t.Fatalf("exit_code %d", missing.ExitCode)
	}
	assertNoRemoteTemp(t, missingOut, before)
}

func remoteArgs(repo, from, to, out string) []string {
	return []string{
		"--repo", repo, "--chart", "demo", "--from", from, "--to", to,
		"--api-server", "off", "--offline", "--current-k8s", "1.35",
		"--format", "json", "--out", out,
	}
}

func packageRemote(t *testing.T, dest, version, template string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "demo")
	writeFile(t, filepath.Join(src, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: "+version+"\n")
	writeFile(t, filepath.Join(src, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(src, "templates", "obj.yaml"), template)
	cmd := exec.Command("helm", "package", src, "--destination", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package %s: %v\n%s", version, err, out)
	}
}

const configMapChart = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  ok: \"yes\"\n"

const removedAPIChart = "apiVersion: extensions/v1beta1\nkind: Deployment\nmetadata:\n  name: demo\n"

func runCheckCapture(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	code := runCheck(args)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	stdout, _ := io.ReadAll(outR)
	stderr, _ := io.ReadAll(errR)
	return code, string(stdout), string(stderr)
}

func readVerdict(t *testing.T, path string) verdict.Report {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep verdict.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report: %v\n%s", err, raw)
	}
	return rep
}

func remoteTempNames() map[string]bool {
	names := map[string]bool{}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return names
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "stageproof-remote-") {
			names[entry.Name()] = true
		}
	}
	return names
}

func assertNoRemoteTemp(t *testing.T, reportPath string, before map[string]bool) {
	t.Helper()
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stageproof-remote-") {
		t.Fatalf("report leaked a temp dir:\n%s", raw)
	}
	for name := range remoteTempNames() {
		if !before[name] {
			t.Fatalf("leaked temp dir %s", name)
		}
	}
}
