package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestRequiredRenderFailureScrubsUserValues(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	secret := "super-secret-token"
	base := t.TempDir()
	head := t.TempDir()
	writeValuesChart(t, base, "enabled: false\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  ok: \"yes\"\n")
	writeValuesChart(t, head, "enabled: false\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  token: {{ with .Values.enabled }}{{ required \"token is required\" $.Values.token }}{{ else }}\"none\"{{ end }}\n")
	values := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(values, []byte("enabled: true\npassword: "+secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), Input{
		BaseDir:    base,
		HeadDir:    head,
		BaseRef:    base,
		HeadRef:    head,
		HeadValues: []string{values},
		BaseValues: []string{values},
		Started:    time.Now(),
	})
	if rep.Verdict != verdict.Blocked || !rep.HasRule("helm.values.new_required_unset") {
		t.Fatalf("verdict %s findings %+v", rep.Verdict, rep.Findings)
	}
	if rep.HasRule("helm.template.render_failed") {
		t.Fatal("required failure was reported as a generic render failure")
	}
	blob := rep.Findings[0].Message + rep.Findings[0].Detail
	for _, f := range rep.Findings {
		blob += f.Message + f.Detail
	}
	if strings.Contains(blob, secret) {
		t.Fatalf("report contained the user value: %s", blob)
	}

	off := filepath.Join(t.TempDir(), "off.yaml")
	if err := os.WriteFile(off, []byte("enabled: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	control := Run(context.Background(), Input{
		BaseDir:    base,
		HeadDir:    head,
		HeadValues: []string{off},
		BaseValues: []string{off},
		Started:    time.Now(),
	})
	if control.HasRule("helm.values.new_required_unset") {
		t.Fatalf("disabled required still fired: %+v", control.Findings)
	}
}

func writeValuesChart(t *testing.T, dir, values, tmpl string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: v2\nname: web\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(values), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates/obj.yaml"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
}
