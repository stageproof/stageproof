package helm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareRendersMasksAClockAndReportsARealDifference(t *testing.T) {
	left, right := helmPair(t)
	ctx := context.Background()

	same := CompareRenders(ctx, left, right, chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  ok: \"yes\"\n"), nil)
	if same.Failed || len(same.Effects) != 0 {
		t.Fatalf("static chart: failed=%v failure=%s effects=%v", same.Failed, same.Failure, same.Effects)
	}

	clock := CompareRenders(ctx, left, right, chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  stamp: {{ now | quote }}\n"), nil)
	if clock.Failed {
		t.Fatal(clock.Failure)
	}
	for _, e := range clock.Effects {
		if strings.Contains(e.Path, "stamp") {
			t.Fatalf("volatile field was compared: %+v", e)
		}
	}

	version := CompareRenders(ctx, left, right, chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  helm: {{ .Capabilities.HelmVersion.Version | quote }}\n"), nil)
	if version.Failed || len(version.Effects) == 0 {
		t.Fatalf("version chart: failed=%v failure=%s", version.Failed, version.Failure)
	}

	// mustToYaml is defined in Helm 4 and not in Helm 3. The Helm 3 side fails.
	broken := CompareRenders(ctx, right, left, chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  y: {{ mustToYaml .Values | quote }}\n"), []string{"hunter2secret"})
	if !broken.Failed {
		t.Fatal("mustToYaml should fail on Helm 3")
	}
	if strings.Contains(broken.Failure, "hunter2secret") {
		t.Fatal(broken.Failure)
	}
}

func TestCompareScrubsAValueHelmPrints(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	opts := chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  x: {{ fail .Values.password }}\n")
	opts.SetValues = []string{"password=hunter2secret"}
	got := CompareRenders(context.Background(), "helm", "helm", opts, []string{"hunter2secret"})
	if !got.Failed {
		t.Fatal("expected the template to fail")
	}
	if strings.Contains(got.Failure, "hunter2secret") {
		t.Fatal(got.Failure)
	}
}

func TestCommandsStageproofRunsExist(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	cases := []struct {
		cmd   string
		flags []string
	}{
		{"template", []string{"--namespace", "--include-crds", "--skip-crds", "--kube-version", "--api-versions", "--is-upgrade", "--set", "--set-string", "--set-file"}},
		{"pull", []string{"--version", "--untar", "--untardir", "--repo", "--plain-http"}},
		{"upgrade", []string{"--install", "--wait", "--timeout", "--namespace"}},
		{"install", []string{"--wait", "--timeout", "--namespace"}},
		{"uninstall", []string{"--ignore-not-found", "--namespace", "--wait", "--timeout"}},
		{"rollback", []string{"--namespace", "--wait", "--timeout"}},
		{"status", []string{"--namespace"}},
		{"test", []string{"--namespace", "--timeout"}},
	}
	for _, tc := range cases {
		out, err := exec.Command("helm", tc.cmd, "--help").CombinedOutput()
		if err != nil {
			t.Fatalf("helm %s --help: %v\n%s", tc.cmd, err, out)
		}
		text := string(out)
		for _, flag := range tc.flags {
			if !strings.Contains(text, flag) {
				t.Errorf("helm %s help lacks %s", tc.cmd, flag)
			}
		}
	}
	ver, err := exec.Command("helm", "version", "--template", "{{.Version}}").Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(ver)), "v") {
		t.Fatalf("version template: %v %q", err, ver)
	}
	opts := chartOptions(t, "v2", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: c\ndata:\n  ok: \"yes\"\n")
	opts.KubeVersion = "1.35"
	opts.SetValues = []string{"color=blue"}
	if _, err := RenderChart(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
}

func helmPair(t *testing.T) (string, string) {
	t.Helper()
	left := "/tmp/helm-bins/helm3"
	right := "/tmp/helm-bins/helm4"
	if _, err := os.Stat(left); err != nil {
		t.Skip("helm 3 binary is not installed")
	}
	if _, err := os.Stat(right); err != nil {
		t.Skip("helm 4 binary is not installed")
	}
	return left, right
}

func chartOptions(t *testing.T, api, template string) RenderOptions {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte("apiVersion: "+api+"\nname: c\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "c.yaml"), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
	return RenderOptions{ChartDir: dir, ReleaseName: "rel", Namespace: "ns"}
}
