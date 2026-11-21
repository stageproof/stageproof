package flux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/helm"
	"gopkg.in/yaml.v3"
)

func TestReleaseNameCompositionAndHash(t *testing.T) {
	if got := ReleaseName("", "web", ""); got != "web" {
		t.Fatalf("name: %s", got)
	}
	if got := ReleaseName("", "web", "custom"); got != "custom" {
		t.Fatalf("explicit: %s", got)
	}
	if got := ReleaseName("apps", "web", ""); got != "apps-web" {
		t.Fatalf("target namespace: %s", got)
	}
	got := ReleaseName("a-very-lengthy-target-namespace", "with-a-nice-object-name", "")
	if !strings.HasSuffix(got, "-97af5d7f41f3") || len(got) > 53 {
		t.Fatalf("shortened name: %s", got)
	}
}

func TestV2Beta1ChartSpecParses(t *testing.T) {
	rel, _, err := ParseBytes([]byte(betaRelease()), "apps/web.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Chart.Mode != "template" || rel.Chart.Chart != "charts/web" || rel.Chart.SourceRef.Kind != "GitRepository" {
		t.Fatalf("%+v", rel.Chart)
	}
	if rel.UpgradeCRDs != "" {
		t.Fatalf("omitted upgrade crds: %q", rel.UpgradeCRDs)
	}
}

func TestUpgradeCRDsExplicit(t *testing.T) {
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  upgrade:\n    crds: CreateReplace\n  chart:", 1)
	rel, _, err := ParseBytes([]byte(raw), "apps/web.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel.UpgradeCRDs != "CreateReplace" {
		t.Fatalf("policy %q", rel.UpgradeCRDs)
	}
}

func TestBothChartAndChartRefAreNotChecked(t *testing.T) {
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  chartRef:\n    kind: OCIRepository\n    name: web\n  chart:", 1)
	_, _, err := ParseBytes([]byte(raw), "apps/web.yaml", nil)
	var nc *NotCheckedError
	if !errors.As(err, &nc) || !strings.Contains(err.Error(), "both spec.chart and spec.chartRef") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaceholderInVersionIsUnresolved(t *testing.T) {
	raw := strings.Replace(betaRelease(), "chart: charts/web", "chart: charts/web\n      version: ${chart_version}", 1)
	_, _, err := ParseBytes([]byte(raw), "apps/web.yaml", nil)
	if tokens := UnresolvedTokens(err); len(tokens) != 1 || tokens[0] != "${chart_version}" {
		t.Fatalf("err = %v", err)
	}
}

func TestPlaceholderInValuesIsReplaced(t *testing.T) {
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  values:\n    color: ${color}\n  chart:", 1)
	rel, used, err := ParseBytes([]byte(raw), "apps/web.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(used) != 1 || used[0] != "${color}" || rel.Inline["color"] != fixedPlaceholder {
		t.Fatalf("used %v inline %#v", used, rel.Inline)
	}
}

func TestConfiguredPlaceholderFillsTheVersion(t *testing.T) {
	raw := strings.Replace(betaRelease(), "chart: charts/web", "chart: charts/web\n      version: ${chart_version}", 1)
	rel, used, err := ParseBytes([]byte(raw), "apps/web.yaml", map[string]string{"${chart_version}": "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.Chart.Version != "1.2.3" || len(used) != 0 {
		t.Fatalf("version %q used %v", rel.Chart.Version, used)
	}
}

func TestOtherGitRepoIsNotCloned(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeFile(t, filepath.Join(root, "gitrepo.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: charts\nspec:\n  url: https://127.0.0.1:1/other.git\n")
	rel := mustParse(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err := Materialize(ctx, rel, MaterializeOptions{RepoRoot: root, Remotes: []string{"https://example.test/stageproof.git"}})
	if time.Since(started) > time.Second {
		t.Fatalf("took %s, a clone or a pull would still be running", time.Since(started))
	}
	var nc *NotCheckedError
	if !errors.As(err, &nc) || !strings.Contains(err.Error(), "does not clone") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingChartValuesFileIsNotRenderedFromDefaults(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	raw := strings.Replace(betaRelease(), "sourceRef:", "valuesFiles:\n        - charts/web/missing.yaml\n      sourceRef:", 1)
	rel, _, err := ParseBytes([]byte(raw), filepath.Join(root, "apps/web.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Materialize(context.Background(), rel, opt(root))
	var nc *NotCheckedError
	if !errors.As(err, &nc) || !strings.Contains(err.Error(), "not rendered from its defaults") {
		t.Fatalf("err = %v", err)
	}
}

func TestIgnoreMissingChartValuesUsesTheChart(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	raw := strings.Replace(betaRelease(), "sourceRef:", "ignoreMissingValuesFiles: true\n      valuesFiles:\n        - charts/web/missing.yaml\n      sourceRef:", 1)
	out := mustRender(t, root, raw)
	if !strings.Contains(out, `color: "chart"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestChartValuesFilesReplaceTheChartDefault(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	writeFile(t, filepath.Join(root, "charts/web/a.yaml"), "color: first\nsize: first\n")
	writeFile(t, filepath.Join(root, "charts/web/b.yaml"), "color: second\n")
	raw := strings.Replace(betaRelease(), "sourceRef:", "valuesFiles:\n        - charts/web/a.yaml\n        - charts/web/b.yaml\n      sourceRef:", 1)
	out := mustRender(t, root, raw)
	if !strings.Contains(out, `color: "second"`) || !strings.Contains(out, `size: "first"`) || strings.Contains(out, `size: "chart"`) {
		t.Fatalf("last file should win and the chart default should be absent:\n%s", out)
	}
	kept, err := os.ReadFile(filepath.Join(root, "charts/web/values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != "color: chart\nsize: chart\n" {
		t.Fatalf("working tree values.yaml was overwritten: %s", kept)
	}
}

func TestValuesFromOrderThenInlineThenTargetPath(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	writeFile(t, filepath.Join(root, "first.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: first\ndata:\n  values.yaml: |\n    color: first\n    size: first\n")
	writeFile(t, filepath.Join(root, "second.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: second\ndata:\n  values.yaml: |\n    color: second\n")
	writeFile(t, filepath.Join(root, "path.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: path\ndata:\n  color: frompath\n")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", `spec:
  values:
    color: inline
    size: inline
  valuesFrom:
    - kind: ConfigMap
      name: first
    - kind: ConfigMap
      name: second
    - kind: ConfigMap
      name: path
      valuesKey: color
      targetPath: color
  chart:`, 1)
	out := mustRender(t, root, raw)
	if !strings.Contains(out, `color: "frompath"`) || !strings.Contains(out, `size: "inline"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestOptionalMissingValuesFromIsSkipped(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: missing\n      optional: true\n  chart:", 1)
	rel, _, err := ParseBytes([]byte(raw), filepath.Join(root, "apps/web.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	mat, err := Materialize(context.Background(), rel, opt(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	if len(mat.Gaps) != 0 {
		t.Fatalf("gaps %+v", mat.Gaps)
	}
}

func TestMissingValuesFromIsNamedAndDoesNotInventValues(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: prod-values\n  chart:", 1)
	rel, _, err := ParseBytes([]byte(raw), filepath.Join(root, "apps/web.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	mat, err := Materialize(context.Background(), rel, opt(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	if len(mat.Gaps) != 1 || !strings.Contains(mat.Gaps[0], "ConfigMap/default/prod-values") {
		t.Fatalf("gaps %+v", mat.Gaps)
	}
	out := renderMat(t, mat)
	if !strings.Contains(out, `color: "chart"`) {
		t.Fatalf("chart default should still render, with the gap recorded:\n%s", out)
	}
}

func TestSecretValuesAreNotPutInTheGap(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	secret := "stageproof-secret-value"
	writeFile(t, filepath.Join(root, "secret.yaml"), "apiVersion: v1\nkind: Secret\nmetadata:\n  name: prod\nstringData:\n  values.yaml: |\n    color: "+secret+"\n")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: Secret\n      name: prod\n  chart:", 1)
	rel := mustParseRaw(t, root, raw)
	mat, err := Materialize(context.Background(), rel, opt(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	blob := strings.Join(mat.Gaps, " ")
	for _, label := range mat.Labels {
		blob += label
	}
	if strings.Contains(blob, secret) {
		t.Fatalf("secret leaked into the report labels: %s", blob)
	}
	rawValues, err := os.ReadFile(mat.ValueFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytesContains(rawValues, secret) {
		t.Fatalf("the secret was not used for the render:\n%s", rawValues)
	}
}

func TestEncryptedSecretIsNotDecrypted(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	cipher := "ENC[not-a-real-ciphertext-0123456789]"
	writeFile(t, filepath.Join(root, "secret.yaml"), "apiVersion: v1\nkind: Secret\nmetadata:\n  name: prod\nstringData:\n  values.yaml: |\n    color: "+cipher+"\nsops:\n  mac: abc\n")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: Secret\n      name: prod\n  chart:", 1)
	rel := mustParseRaw(t, root, raw)
	mat, err := Materialize(context.Background(), rel, opt(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	joined := strings.Join(mat.Gaps, " ")
	if !strings.Contains(joined, "does not decrypt") || strings.Contains(joined, cipher) {
		t.Fatalf("gaps %s", joined)
	}
	if len(mat.ValueFiles) != 0 {
		t.Fatal("encrypted values were written")
	}
}

func TestLiteralTargetPathKeepsDigits(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	writeFile(t, filepath.Join(root, "code.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: code\ndata:\n  code: \"01\"\n")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: code\n      valuesKey: code\n      targetPath: code\n      literal: true\n  chart:", 1)
	out := mustRender(t, root, raw)
	if !strings.Contains(out, `code: "01"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestReleaseNameIsPassedToHelm(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	raw := strings.Replace(betaRelease(), "metadata:\n  name: web", "metadata:\n  name: web\n  namespace: team", 1)
	raw = strings.Replace(raw, "spec:\n  chart:", "spec:\n  targetNamespace: apps\n  chart:", 1)
	out := mustRender(t, root, raw)
	if !strings.Contains(out, "release: apps-web") || !strings.Contains(out, "namespace: apps") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestReleasesReferencingFindsTheConfigMap(t *testing.T) {
	root := t.TempDir()
	writeChart(t, root)
	writeGitRepo(t, root, "https://example.test/stageproof.git")
	writeFile(t, filepath.Join(root, "values/prod.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: prod-values\ndata:\n  values.yaml: |\n    color: blue\n")
	raw := strings.Replace(betaRelease(), "spec:\n  chart:", "spec:\n  valuesFrom:\n    - kind: ConfigMap\n      name: prod-values\n  chart:", 1)
	writeFile(t, filepath.Join(root, "apps/web.yaml"), raw)
	got := ReleasesReferencing(root, []string{"values/prod.yaml"}, nil)
	if len(got) != 1 || got[0] != "apps/web.yaml" {
		t.Fatalf("got %v", got)
	}
}

func TestMergeIsDeterministic(t *testing.T) {
	a := mergeValues(nil, map[string]any{"b": "1", "a": "2"})
	buf, err := yaml.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := yaml.Marshal(mergeValues(nil, map[string]any{"b": "1", "a": "2"}))
	if err != nil || string(buf) != string(again) {
		t.Fatalf("%s vs %s (%v)", buf, again, err)
	}
}

func betaRelease() string {
	return `apiVersion: helm.toolkit.fluxcd.io/v2beta1
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
`
}

func opt(root string) MaterializeOptions {
	return MaterializeOptions{RepoRoot: root, Remotes: []string{"https://example.test/stageproof.git"}}
}

func mustParse(t *testing.T, root string) *Release {
	t.Helper()
	return mustParseRaw(t, root, betaRelease())
}

func mustParseRaw(t *testing.T, root, raw string) *Release {
	t.Helper()
	rel, _, err := ParseBytes([]byte(raw), filepath.Join(root, "apps/web.yaml"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func mustRender(t *testing.T, root, raw string) string {
	t.Helper()
	rel := mustParseRaw(t, root, raw)
	mat, err := Materialize(context.Background(), rel, opt(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	return renderMat(t, mat)
}

func renderMat(t *testing.T, mat *Materialized) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	out, err := helm.RenderChart(context.Background(), helm.RenderOptions{
		ReleaseName: mat.ReleaseName,
		Namespace:   mat.Namespace,
		ChartDir:    mat.ChartDir,
		ValuesFiles: mat.ValueFiles,
		KubeVersion: "1.33",
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeChart(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "charts/web/Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(root, "charts/web/values.yaml"), "color: chart\nsize: chart\n")
	writeFile(t, filepath.Join(root, "charts/web/templates/cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: web
  namespace: {{ .Release.Namespace }}
data:
  color: {{ .Values.color | quote }}
  size: {{ .Values.size | default "" | quote }}
  code: {{ .Values.code | default "chart" | quote }}
  release: {{ .Release.Name }}
`)
}

func writeGitRepo(t *testing.T, root, url string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "sources/git.yaml"), "apiVersion: source.toolkit.fluxcd.io/v1\nkind: GitRepository\nmetadata:\n  name: charts\nspec:\n  url: "+url+"\n")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func bytesContains(raw []byte, s string) bool {
	return strings.Contains(string(raw), s)
}
