package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestCheckPRFluxHelmRepositoryVersionBump(t *testing.T) {
	repo, srv := fluxGitRepo(t)
	chart := filepath.Join(repo, "srcchart")
	writeFluxChart(t, chart, "1.0.0", "1.27", "one")
	pkg := t.TempDir()
	helmCmd(t, repo, "package", chart, "--destination", pkg)
	writeFluxChart(t, chart, "1.1.0", "1.28", "two")
	helmCmd(t, repo, "package", chart, "--destination", pkg)
	helmCmd(t, repo, "repo", "index", pkg, "--url", srv.URL)
	srv.Config.Handler = http.FileServer(http.Dir(pkg))

	writeFile(t, filepath.Join(repo, "sources/charts.yaml"), fluxHelmRepo(srv.URL))
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), fluxRelease("1.0.0"))
	gitCommitAll(t, repo, "baseline")
	git(t, "-C", repo, "checkout", "-q", "-b", "change")
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), fluxRelease("1.1.0"))
	gitCommitAll(t, repo, "bump the chart")

	raw := runFluxPR(t, repo)
	if !strings.Contains(raw, `"adapter": "flux"`) && !strings.Contains(raw, `"adapter":"flux"`) {
		t.Fatalf("adapter:\n%s", raw)
	}
	if !strings.Contains(raw, "helm.image.changed") {
		t.Fatalf("versions were not both rendered:\n%s", raw)
	}
	if !strings.Contains(raw, "crd.flux_upgrade_crds") || !strings.Contains(raw, "CreateReplace") {
		t.Fatalf("upgrade.crds was not wired:\n%s", raw)
	}
	var combined report.Combined
	if err := json.Unmarshal([]byte(raw), &combined); err != nil {
		t.Fatal(err)
	}
	if combined.Reports[0].Verdict != verdict.Passed {
		t.Fatalf("verdict %s\n%s", combined.Reports[0].Verdict, raw)
	}
}

func TestCheckPRFluxOCIRepositoryTagBump(t *testing.T) {
	repo, _ := fluxGitRepo(t)
	reg := newOCIRegistry(t)
	chart := filepath.Join(repo, "srcchart")
	writeFluxChart(t, chart, "1.0.0", "1.27", "one")
	reg.add(t, repo, chart, "1.0.0")
	writeFluxChart(t, chart, "1.1.0", "1.28", "two")
	reg.add(t, repo, chart, "1.1.0")

	writeFile(t, filepath.Join(repo, "sources/web.yaml"), fluxOCI(reg.url, "1.0.0"))
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), fluxChartRef())
	gitCommitAll(t, repo, "baseline")
	git(t, "-C", repo, "checkout", "-q", "-b", "change")
	writeFile(t, filepath.Join(repo, "sources/web.yaml"), fluxOCI(reg.url, "1.1.0"))
	gitCommitAll(t, repo, "bump the tag")

	raw := runFluxPR(t, repo)
	if !strings.Contains(raw, "helm.image.changed") || !strings.Contains(raw, ":1.0.0") || !strings.Contains(raw, ":1.1.0") {
		reg.t.Logf("requests: %s", strings.Join(reg.requests, "\n"))
		t.Fatalf("oci source was not checked:\n%s", raw)
	}
}

func TestCheckPRFluxMissingValuesFrom(t *testing.T) {
	repo, srv := fluxGitRepo(t)
	chart := filepath.Join(repo, "srcchart")
	writeFluxChart(t, chart, "1.0.0", "1.27", "one")
	pkg := t.TempDir()
	helmCmd(t, repo, "package", chart, "--destination", pkg)
	helmCmd(t, repo, "repo", "index", pkg, "--url", srv.URL)
	srv.Config.Handler = http.FileServer(http.Dir(pkg))
	writeFile(t, filepath.Join(repo, "sources/charts.yaml"), fluxHelmRepo(srv.URL))
	body := strings.Replace(fluxRelease("1.0.0"), "spec:\n", "spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: prod-values\n", 1)
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), body)
	gitCommitAll(t, repo, "baseline")
	git(t, "-C", repo, "checkout", "-q", "-b", "change")
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), strings.Replace(body, "name: web", "name: web\n  annotations: {}\n", 1))
	gitCommitAll(t, repo, "touch the release")

	code, raw := runFluxPRCode(t, repo)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	if !strings.Contains(raw, "flux.values.unreadable") || !strings.Contains(raw, "ConfigMap/default/prod-values") {
		t.Fatalf("missing valuesFrom was not named:\n%s", raw)
	}
	if strings.Contains(raw, `"verdict": "PROVEN"`) || strings.Contains(raw, `"verdict":"PROVEN"`) {
		t.Fatalf("incomplete values were called PROVEN:\n%s", raw)
	}
}

func TestCheckPRFluxValuesFileChange(t *testing.T) {
	repo, _ := fluxGitRepo(t)
	writeFile(t, filepath.Join(repo, "charts/web/Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(repo, "charts/web/values.yaml"), "tag: \"1.0\"\n")
	writeFile(t, filepath.Join(repo, "charts/web/templates/deploy.yaml"), `apiVersion: apps/v1
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
          image: nginx:{{ .Values.tag | required "tag" }}
`)
	writeFile(t, filepath.Join(repo, "sources/git.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: charts\nspec:\n  url: https://example.test/stageproof.git\n")
	writeFile(t, filepath.Join(repo, "values/prod.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: prod-values\ndata:\n  values.yaml: |\n    tag: \"1.27\"\n")
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
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
  valuesFrom:
    - kind: ConfigMap
      name: prod-values
`)
	gitCommitAll(t, repo, "baseline")
	git(t, "-C", repo, "checkout", "-q", "-b", "change")
	writeFile(t, filepath.Join(repo, "values/prod.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: prod-values\ndata:\n  values.yaml: |\n    tag: \"1.28\"\n")
	gitCommitAll(t, repo, "bump the tag")

	raw := runFluxPR(t, repo)
	if !strings.Contains(raw, "helm.image.changed") || !strings.Contains(raw, "prod-values") {
		t.Fatalf("the values ConfigMap was not read from both revisions:\n%s", raw)
	}
}

func TestCheckPRFluxExternalGitIsNotChecked(t *testing.T) {
	repo, _ := fluxGitRepo(t)
	writeFile(t, filepath.Join(repo, "sources/git.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: other\nspec:\n  url: https://127.0.0.1:1/other.git\n")
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
spec:
  chart:
    spec:
      chart: charts/web
      sourceRef:
        kind: GitRepository
        name: other
`)
	gitCommitAll(t, repo, "baseline")
	git(t, "-C", repo, "checkout", "-q", "-b", "change")
	writeFile(t, filepath.Join(repo, "apps/web.yaml"), strings.Replace(mustRead(t, filepath.Join(repo, "apps/web.yaml")), "name: web", "name: web\n  annotations: {}\n", 1))
	gitCommitAll(t, repo, "touch")

	code, raw := runFluxPRCode(t, repo)
	if code != 2 || !strings.Contains(raw, "does not clone") {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	if strings.Contains(raw, "stageproof-flux-") {
		t.Fatalf("a temp chart was rendered:\n%s", raw)
	}
}

func fluxGitRepo(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	for _, bin := range []string{"git", "helm"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	repo := t.TempDir()
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	git(t, "-C", repo, "init", "-q", "-b", "main")
	git(t, "-C", repo, "remote", "add", "origin", "https://example.test/stageproof.git")
	return repo, srv
}

func gitCommitAll(t *testing.T, repo, msg string) {
	t.Helper()
	git(t, "-C", repo, "add", "-A")
	git(t, "-C", repo, "commit", "-q", "-m", msg)
}

func runFluxPR(t *testing.T, repo string) string {
	t.Helper()
	code, raw := runFluxPRCode(t, repo)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, raw)
	}
	return raw
}

func runFluxPRCode(t *testing.T, repo string) (int, string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	out := filepath.Join(repo, "report.json")
	code := runCheckPR([]string{"--current-git", "main", "--api-server", "off", "--out", out})
	raw, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("exit %d, no report: %v", code, readErr)
	}
	return code, string(raw)
}

func helmCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("helm", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helm %v: %v\n%s", args, err, out)
	}
}

func writeFluxChart(t *testing.T, dir, version, tag, note string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: web\nversion: "+version+"\n")
	writeFile(t, filepath.Join(dir, "values.yaml"), "tag: \""+tag+"\"\n")
	writeFile(t, filepath.Join(dir, "templates/deploy.yaml"), `apiVersion: apps/v1
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
	writeFile(t, filepath.Join(dir, "crds/widgets.yaml"), `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.test
spec:
  group: example.test
  scope: Namespaced
  names:
    kind: Widget
    plural: widgets
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          description: `+note+"\n")
}

func fluxHelmRepo(url string) string {
	return "apiVersion: source.toolkit.fluxcd.io/v1\nkind: HelmRepository\nmetadata:\n  name: charts\nspec:\n  interval: 10m\n  url: " + url + "\n"
}

func fluxRelease(version string) string {
	return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
spec:
  interval: 10m
  upgrade:
    crds: CreateReplace
  chart:
    spec:
      chart: web
      version: "` + version + `"
      sourceRef:
        kind: HelmRepository
        name: charts
`
}

func fluxChartRef() string {
	return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: web
spec:
  interval: 10m
  chartRef:
    kind: OCIRepository
    name: web
`
}

func fluxOCI(url, tag string) string {
	return `apiVersion: source.toolkit.fluxcd.io/v1
kind: OCIRepository
metadata:
  name: web
spec:
  interval: 10m
  insecure: true
  url: ` + url + `
  ref:
    tag: "` + tag + `"
`
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type ociRegistry struct {
	url      string
	srv      *httptest.Server
	blobs    map[string][]byte
	manifest map[string][]byte
	requests []string
	t        *testing.T
}

func newOCIRegistry(t *testing.T) *ociRegistry {
	t.Helper()
	reg := &ociRegistry{
		blobs:    map[string][]byte{},
		manifest: map[string][]byte{},
		t:        t,
	}
	reg.srv = httptest.NewServer(http.HandlerFunc(reg.serve))
	t.Cleanup(reg.srv.Close)
	reg.url = "oci://" + strings.TrimPrefix(reg.srv.URL, "http://") + "/web"
	return reg
}

func (r *ociRegistry) add(t *testing.T, repo, chart, version string) {
	t.Helper()
	pkg := t.TempDir()
	helmCmd(t, repo, "package", chart, "--destination", pkg)
	tgz, err := os.ReadFile(filepath.Join(pkg, "web-"+version+".tgz"))
	if err != nil {
		t.Fatal(err)
	}
	config := []byte(`{"name":"web","version":"` + version + `"}`)
	configDigest := digest(config)
	layerDigest := digest(tgz)
	r.blobs[configDigest] = config
	r.blobs[layerDigest] = tgz
	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.cncf.helm.config.v1+json",
			"digest":    configDigest,
			"size":      len(config),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip",
			"digest":    layerDigest,
			"size":      len(tgz),
		}},
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.manifest[version] = body
	r.manifest[digest(body)] = body
}

func (r *ociRegistry) serve(w http.ResponseWriter, req *http.Request) {
	r.requests = append(r.requests, req.Method+" "+req.URL.Path)
	if req.URL.Path == "/v2/" || req.URL.Path == "/v2" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
		return
	}
	if strings.Contains(req.URL.Path, "/blobs/") {
		sum := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		body, ok := r.blobs[sum]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Docker-Content-Digest", sum)
		w.Header().Set("Content-Type", "application/octet-stream")
		if req.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	if strings.Contains(req.URL.Path, "/manifests/") {
		ref := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
		body, ok := r.manifest[ref]
		if !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", digest(body))
		if req.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	http.NotFound(w, req)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
