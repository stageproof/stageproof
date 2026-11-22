package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A top-level charts/ directory is the most common repository layout. Only a
// charts/ directory inside a chart holds subcharts.
func TestDiscoverFindsChartsInTopLevelChartsDir(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "charts", "webapp", "Chart.yaml"), "apiVersion: v2\nname: webapp\nversion: 1.0.0\n")
	write(t, filepath.Join(root, "charts", "webapp", "values.yaml"), "replicas: 1\n")
	write(t, filepath.Join(root, "charts", "webapp", "values-prod.yaml"), "replicas: 3\n")
	write(t, filepath.Join(root, "charts", "webapp", "charts", "redis", "Chart.yaml"), "apiVersion: v2\nname: redis\nversion: 1.0.0\n")
	write(t, filepath.Join(root, "deploy", "api", "Chart.yaml"), "apiVersion: v2\nname: api\nversion: 1.0.0\n")
	write(t, filepath.Join(root, "node_modules", "pkg", "Chart.yaml"), "apiVersion: v2\nname: vendored\nversion: 1.0.0\n")

	cfg, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, ch := range cfg.Charts {
		paths = append(paths, ch.Path)
	}
	if len(paths) != 2 || paths[0] != "charts/webapp" || paths[1] != "deploy/api" {
		t.Fatalf("expected charts/webapp and deploy/api, the subchart and node_modules skipped; got %v", paths)
	}
	if got := cfg.Charts[0].ValuesFiles; len(got) != 2 || got[0] != "charts/webapp/values-prod.yaml" || got[1] != "charts/webapp/values.yaml" {
		t.Fatalf("values files for charts/webapp = %v", got)
	}
}

func TestValuesFilesForReturnsOnlyExistingFilesOfThatChart(t *testing.T) {
	root := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	write(t, "charts/webapp/values-prod.yaml", "replicas: 3\n")
	cfg := &Config{Charts: []Chart{
		{Name: "webapp", Path: "charts/webapp", ValuesFiles: []string{"charts/webapp/values-prod.yaml", "charts/webapp/values-gone.yaml"}},
		{Name: "api", Path: "charts/api", ValuesFiles: []string{"charts/api/values.yaml"}},
	}}

	if _, err := cfg.ValuesFilesFor("./charts/webapp/"); err == nil || !strings.Contains(err.Error(), "values-gone.yaml") {
		t.Fatalf("a listed missing values file must be an error, got %v", err)
	}
	cfg.Charts[0].ValuesFiles = []string{"charts/webapp/values-prod.yaml"}
	got, err := cfg.ValuesFilesFor("./charts/webapp/")
	if err != nil || len(got) != 1 || got[0] != "charts/webapp/values-prod.yaml" {
		t.Fatalf("ValuesFilesFor(charts/webapp) = %v %v", got, err)
	}
	if got, err := cfg.ValuesFilesFor("charts/unknown"); err != nil || got != nil {
		t.Fatalf("an unlisted chart has no configured values files, got %v %v", got, err)
	}
	var none *Config
	if got, err := none.ValuesFilesFor("charts/webapp"); err != nil || got != nil {
		t.Fatalf("a missing config has no values files, got %v %v", got, err)
	}
	empty := &Config{Charts: []Chart{{Name: "webapp", Path: "charts/webapp"}}}
	if got, err := empty.ValuesFilesFor("charts/webapp"); err != nil || got != nil {
		t.Fatalf("a chart with no valuesFiles list falls back, got %v %v", got, err)
	}
}

func TestSmokeValidation(t *testing.T) {
	ok, err := LoadBytes("x", []byte("smoke:\n  - name: deploy\n    run: kubectl get deploy\n"))
	if err != nil || len(ok.Smoke) != 1 {
		t.Fatalf("omitted timeout must load, got %+v %v", ok, err)
	}
	d, err := ok.Smoke[0].Duration()
	if err != nil || d != DefaultSmokeTimeout {
		t.Fatalf("default timeout = %s %v", d, err)
	}
	if _, err := LoadBytes("x", []byte("smoke:\n  - run: kubectl get deploy\n")); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("missing name: %v", err)
	}
	if _, err := LoadBytes("x", []byte("smoke:\n  - name: deploy\n")); err == nil || !strings.Contains(err.Error(), "run is required") {
		t.Fatalf("missing run: %v", err)
	}
	if _, err := LoadBytes("x", []byte("smoke:\n  - name: deploy\n    run: true\n    timeout: nope\n")); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("bad timeout: %v", err)
	}
	if _, err := LoadBytes("x", []byte("smoke:\n  - name: deploy\n    run: true\n    timeout: 0s\n")); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("zero timeout: %v", err)
	}
}
