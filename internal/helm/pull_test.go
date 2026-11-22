package helm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubUserinfo(t *testing.T) {
	got := scrubUserinfo("get https://user:s3cret@charts.example/index.yaml failed")
	if strings.Contains(got, "s3cret") || strings.Contains(got, "user:") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "https://charts.example/index.yaml") {
		t.Fatal(got)
	}
}

func TestPullFindsChartWhenArchiveNameDiffers(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writePullChart(t, src, "widget", "0.1.0", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	pkg := filepath.Join(root, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("helm", "package", src, "--destination", pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package: %v\n%s", err, out)
	}
	index := `apiVersion: v1
entries:
  demo:
  - apiVersion: v2
    name: demo
    version: 0.1.0
    urls:
    - widget-0.1.0.tgz
`
	if err := os.WriteFile(filepath.Join(pkg, "index.yaml"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(pkg)))
	t.Cleanup(srv.Close)

	dest := filepath.Join(root, "out")
	got, err := Pull(context.Background(), srv.URL, "demo", "0.1.0", dest)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) == "demo" {
		t.Fatalf("unpacked dir followed the requested name, got %s", got)
	}
	if filepath.Base(got) != "widget" {
		t.Fatalf("chart dir = %s", got)
	}
	if _, err := os.Stat(filepath.Join(got, "Chart.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestPullMissingVersion(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writePullChart(t, src, "demo", "0.1.0", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	pkg := filepath.Join(root, "pkg")
	helmPackage(t, src, pkg)
	index := exec.Command("helm", "repo", "index", pkg, "--url", "http://127.0.0.1")
	if out, err := index.CombinedOutput(); err != nil {
		t.Fatalf("index: %v\n%s", err, out)
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(pkg)))
	t.Cleanup(srv.Close)

	_, err := Pull(context.Background(), srv.URL, "demo", "9.9.9", filepath.Join(root, "out"))
	if err == nil {
		t.Fatal("missing version succeeded")
	}
	if strings.Contains(err.Error(), root) {
		t.Fatal(err)
	}
}

func writePullChart(t *testing.T, dir, name, version, template string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "apiVersion: v2\nname: " + name + "\nversion: " + version + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Chart.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "templates", "obj.yaml"), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
}

func helmPackage(t *testing.T, src, dest string) {
	t.Helper()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("helm", "package", src, "--destination", dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("package: %v\n%s", err, out)
	}
}
