package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestIgnoredBlockerDoesNotCount(t *testing.T) {
	ignored := []verdict.Finding{{
		RuleID:   "impact.delete",
		Severity: verdict.SeverityBlocker,
		Ignored:  true,
	}}
	if countBlockers(ignored) != 0 {
		t.Fatal("an ignored blocker still skips --live")
	}
	if countBlockers([]verdict.Finding{{Severity: verdict.SeverityBlocker}}) != 1 {
		t.Fatal("a blocker was not counted")
	}
}

func TestLiveCheckedAppends(t *testing.T) {
	if got := noteLiveChecked(""); got != verdict.CheckedFilesAndLive {
		t.Fatalf("empty: %s", got)
	}
	got := noteLiveChecked("files, API server 1.35.0")
	if got != "files, API server 1.35.0, live cluster" {
		t.Fatalf("append: %s", got)
	}
	if again := noteLiveChecked(got); again != got {
		t.Fatalf("doubled: %s", again)
	}
}

func TestHelmParametersDropIsNotRestoredBySet(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := t.TempDir()
	head := t.TempDir()
	body := func(image string) string {
		return `apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
data:
  image: ` + image + `
`
	}
	writeAuditChart(t, base, body(`{{ .Values.config.logLevel | quote }}`))
	writeAuditChart(t, head, body(`"fixed"`))
	dir := t.TempDir()
	params := filepath.Join(dir, "helm-parameters.yaml")
	if err := os.WriteFile(params, []byte("config:\n  logLevel: warn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), Input{
		BaseDir:    base,
		HeadDir:    head,
		BaseRef:    "v1",
		HeadRef:    "v2",
		HeadValues: []string{params},
		SetValues:  []string{"config.logLevel=warn"},
		HeadSet:    []string{"config.logLevel=warn"},
		Started:    time.Now(),
	})
	if rep.ExitCode != 1 || !rep.HasRule("helm.values.silently_dropped") {
		t.Fatalf("exit %d findings %+v", rep.ExitCode, rep.Findings)
	}
	for _, f := range rep.Findings {
		if f.RuleID == "helm.values.silently_dropped" && f.Proof != "render" {
			t.Fatalf("proof: %+v", f)
		}
	}
}

func writeAuditChart(t *testing.T, dir, tmpl string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: web\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "cm.yaml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
}
