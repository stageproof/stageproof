package config

import (
	"os"
	"path/filepath"
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

	got := cfg.ValuesFilesFor("./charts/webapp/")
	if len(got) != 1 || got[0] != "charts/webapp/values-prod.yaml" {
		t.Fatalf("ValuesFilesFor(charts/webapp) = %v", got)
	}
	if got := cfg.ValuesFilesFor("charts/unknown"); got != nil {
		t.Fatalf("an unlisted chart has no configured values files, got %v", got)
	}
	var none *Config
	if got := none.ValuesFilesFor("charts/webapp"); got != nil {
		t.Fatalf("a missing config has no values files, got %v", got)
	}
}
