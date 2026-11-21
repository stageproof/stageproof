package check

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/k8sdiff"
)

func TestStrictUnknownFollowsTheHelm4InstallDefault(t *testing.T) {
	if strictUnknown(true, 3, false, true) != true {
		t.Fatal("an explicit server-side apply is strict")
	}
	if strictUnknown(false, 4, false, true) != false {
		t.Fatal("a Helm 4 upgrade of an existing release stays client-side")
	}
	if strictUnknown(false, 4, true, false) != false {
		t.Fatal("rendered manifests are not a Helm 4 install")
	}
	if strictUnknown(false, 4, false, false) != true {
		t.Fatal("a new Helm 4 install is server-side apply")
	}
	if strictUnknown(false, 3, false, false) != false {
		t.Fatal("a new Helm 3 install is client-side")
	}
}

func TestChartV3WarnsAndV2DoesNot(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	v3 := t.TempDir()
	writeTinyChart(t, v3, "v3")
	got := Run(context.Background(), Input{
		HeadDir: v3, KubeVersion: "1.35", APIServer: "off", Started: time.Now(),
	})
	if !hasRule(got.Findings, "helm4.chart_v3") {
		t.Fatalf("v3 chart: %#v", got.Findings)
	}
	for _, f := range got.Findings {
		if strings.Contains(f.Message, "hunter") || strings.Contains(f.Detail, "hunter") {
			t.Fatalf("printed a value: %#v", f)
		}
	}
	v2 := t.TempDir()
	writeTinyChart(t, v2, "v2")
	got = Run(context.Background(), Input{
		HeadDir: v2, KubeVersion: "1.35", APIServer: "off", Started: time.Now(),
	})
	if hasRule(got.Findings, "helm4.chart_v3") {
		t.Fatalf("v2 chart warned: %#v", got.Findings)
	}
	if got.ExitCode != 0 {
		t.Fatalf("v2 chart exit %d", got.ExitCode)
	}
}

func TestDescribeEffectsCapsTheList(t *testing.T) {
	effects := make([]k8sdiff.Effect, 0, 9)
	for i := 0; i < 9; i++ {
		effects = append(effects, k8sdiff.Effect{Object: "ConfigMap/default/c", Path: string(rune('a' + i))})
	}
	text := describeEffects(effects)
	if !strings.Contains(text, "and 1 more") {
		t.Fatal(text)
	}
	if strings.Contains(text, "ConfigMap/default/c at i") {
		t.Fatal(text)
	}
	one := describeEffects([]k8sdiff.Effect{{Object: "ConfigMap/default/c", Path: ""}})
	if one != "ConfigMap/default/c is only in one render." {
		t.Fatal(one)
	}
}

func TestHelmCompareAgreesAndDiffers(t *testing.T) {
	other := otherHelm(t)
	sameDir := t.TempDir()
	writeTinyChart(t, sameDir, "v2")
	same := Run(context.Background(), Input{
		HeadDir: sameDir, KubeVersion: "1.35", APIServer: "off", HelmCompare: other, Started: time.Now(),
	})
	if hasRule(same.Findings, "helm4.render_differs") || hasRule(same.Findings, "helm4.render_failed") {
		t.Fatalf("static chart: %#v", same.Findings)
	}
	if same.ExitCode != 0 {
		t.Fatalf("static chart exit %d", same.ExitCode)
	}

	diffDir := t.TempDir()
	writeChartFile(t, diffDir, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  helm: {{ .Capabilities.HelmVersion.Version | quote }}\n")
	diff := Run(context.Background(), Input{
		HeadDir: diffDir, KubeVersion: "1.35", APIServer: "off", HelmCompare: other, Started: time.Now(),
	})
	if !hasRule(diff.Findings, "helm4.render_differs") {
		t.Fatalf("helm version chart: %#v", diff.Findings)
	}
	if diff.ExitCode != 0 {
		t.Fatalf("a render difference is a warning, exit %d", diff.ExitCode)
	}
	for _, f := range diff.Findings {
		if f.RuleID != "helm4.render_differs" {
			continue
		}
		if !strings.Contains(f.Message, "data.helm") {
			t.Fatalf("message: %s", f.Message)
		}
	}
}

func otherHelm(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("helm", "version", "--template", "{{.Version}}").Output()
	if err != nil {
		t.Skip("helm is not installed")
	}
	ver := string(out)
	want := "/tmp/helm-bins/helm4"
	if strings.Contains(ver, "v4.") {
		want = "/tmp/helm-bins/helm3"
	}
	if _, err := os.Stat(want); err != nil {
		t.Skip("second helm binary is not installed")
	}
	return want
}

func writeTinyChart(t *testing.T, dir, api string) {
	t.Helper()
	writeChartFile(t, dir, api, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  ok: \"yes\"\n")
}

func writeChartFile(t *testing.T, dir, api, template string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "apiVersion: " + api + "\nname: c\nversion: 0.1.0\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "c.yaml"), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
}
