package argocd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/helm"
)

func TestCanonicalRepoIgnoresSchemeAndGitSuffix(t *testing.T) {
	want := "github.com/org/repo"
	for _, raw := range []string{
		"https://github.com/org/repo",
		"https://github.com/org/repo.git",
		"http://github.com/org/repo/",
		"git@github.com:org/repo.git",
		"ssh://git@github.com/org/repo.git",
	} {
		if got := canonicalRepo(raw); got != want {
			t.Fatalf("%s -> %s, want %s", raw, got, want)
		}
	}
}

func TestSourcesReplaceSource(t *testing.T) {
	raw := []byte(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  source:
    repoURL: https://charts.example
    chart: ignored
    targetRevision: 0.0.1
  sources:
    - repoURL: https://example.test/stageproof.git
      path: charts/web
`)
	spec, _, err := ParseApplicationBytes(raw, "app.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.LocalPath != "charts/web" || spec.Chart != "" {
		t.Fatalf("spec = %+v", spec)
	}
}

func TestApplicationSetIsNotChecked(t *testing.T) {
	raw := []byte(`apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: demo
spec: {}
`)
	_, _, err := ParseApplicationBytes(raw, "set.yaml", nil)
	var nc *NotCheckedError
	if !errorAs(err, &nc) || !strings.Contains(err.Error(), "ApplicationSet") {
		t.Fatalf("err = %v", err)
	}
}

func TestSkipCrdsPointer(t *testing.T) {
	absent := parseHelm(t, "valueFiles: []\n")
	if absent.SkipCrds != nil {
		t.Fatal("omitted skipCrds must stay nil")
	}
	off := parseHelm(t, "skipCrds: false\n")
	if off.SkipCrds == nil || *off.SkipCrds {
		t.Fatalf("false = %v", off.SkipCrds)
	}
	on := parseHelm(t, "skipCrds: true\n")
	if on.SkipCrds == nil || !*on.SkipCrds {
		t.Fatalf("true = %v", on.SkipCrds)
	}
}

func TestReleaseNameDefaultsToTheApplicationName(t *testing.T) {
	spec := parseHelm(t, "values: |\n  color: blue\n")
	if spec.ReleaseName != "demo" {
		t.Fatalf("release %q", spec.ReleaseName)
	}
	named := parseHelm(t, "releaseName: custom\n")
	if named.ReleaseName != "custom" {
		t.Fatalf("release %q", named.ReleaseName)
	}
}

func TestValuesObjectReplacesValues(t *testing.T) {
	spec := parseHelm(t, `values: |
  color: fromvalues
  size: fromvalues
valuesObject:
  color: fromobject
`)
	if spec.ValuesKind != "valuesObject" {
		t.Fatalf("kind %s values %q", spec.ValuesKind, spec.ValuesYAML)
	}
	if strings.Contains(spec.ValuesYAML, "fromvalues") || !strings.Contains(spec.ValuesYAML, "fromobject") {
		t.Fatalf("values were merged:\n%s", spec.ValuesYAML)
	}
}

func TestOtherRepoIsNotCloned(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	doc := strings.Replace(appDoc("values: |\n  color: blue\n"), "https://example.test/stageproof.git", "https://127.0.0.1:1/other.git", 1)
	spec, _, err := ParseApplicationBytes([]byte(doc), "app.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	_, err = Materialize(ctx, spec, MaterializeOptions{RepoRoot: root, Remotes: []string{"https://example.test/stageproof.git"}})
	if time.Since(started) > time.Second {
		t.Fatalf("took %s, a clone or a pull would still be running", time.Since(started))
	}
	var nc *NotCheckedError
	if !errorAs(err, &nc) || !strings.Contains(err.Error(), "does not clone") {
		t.Fatalf("err = %v", err)
	}
}

func TestMissingValueFileIsNotRenderedFromDefaults(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	_, err := materialize(t, root, appDoc("valueFiles:\n  - missing.yaml\n"))
	var nc *NotCheckedError
	if !errorAs(err, &nc) || !strings.Contains(err.Error(), "not rendered from its defaults") {
		t.Fatalf("err = %v", err)
	}
}

func TestIgnoreMissingValueFileUsesTheChart(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("ignoreMissingValueFiles: true\nvalueFiles:\n  - missing.yaml\n"))
	if !strings.Contains(out, `color: "chart"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestValueFileOverridesChartValues(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "charts", "web", "values-a.yaml"), "color: fromfile\n")
	out := mustRender(t, root, appDoc("valueFiles:\n  - values-a.yaml\n"))
	if !strings.Contains(out, `color: "fromfile"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestLaterValueFileOverridesEarlier(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "charts", "web", "values-a.yaml"), "color: early\n")
	writeFile(t, filepath.Join(root, "charts", "web", "values-b.yaml"), "color: late\n")
	out := mustRender(t, root, appDoc("valueFiles:\n  - values-a.yaml\n  - values-b.yaml\n"))
	if !strings.Contains(out, `color: "late"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestGlobValueFilesAreLexicalAndExplicitWins(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "charts", "web", "values-a.yaml"), "color: early\n")
	writeFile(t, filepath.Join(root, "charts", "web", "values-b.yaml"), "color: late\n")
	globbed := mustRender(t, root, appDoc("valueFiles:\n  - values-*.yaml\n"))
	if !strings.Contains(globbed, `color: "late"`) {
		t.Fatalf("glob output:\n%s", globbed)
	}
	explicit := mustRender(t, root, appDoc("valueFiles:\n  - values-*.yaml\n  - values-a.yaml\n"))
	if !strings.Contains(explicit, `color: "early"`) {
		t.Fatalf("explicit output:\n%s", explicit)
	}
}

func TestValuesOverrideValueFiles(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "charts", "web", "values-a.yaml"), "color: fromfile\n")
	out := mustRender(t, root, appDoc("valueFiles:\n  - values-a.yaml\nvalues: |\n  color: inline\n"))
	if !strings.Contains(out, `color: "inline"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestValuesObjectIgnoresValuesWhenRendered(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("values: |\n  color: fromvalues\n  size: fromvalues\nvaluesObject:\n  color: fromobject\n"))
	if !strings.Contains(out, `color: "fromobject"`) || !strings.Contains(out, `size: "chart"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestParametersOverrideValuesObject(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("valuesObject:\n  color: fromobject\nparameters:\n  - name: color\n    value: fromparam\n"))
	if !strings.Contains(out, `color: "fromparam"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestDuplicateParameterLastWins(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("parameters:\n  - name: color\n    value: one\n  - name: color\n    value: two\n"))
	if !strings.Contains(out, `color: "two"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestForceStringKeepsTheDigits(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("parameters:\n  - name: code\n    value: \"01\"\n    forceString: true\n"))
	if !strings.Contains(out, `code: "01"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestFileParameterOverridesTheChart(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "charts", "web", "params", "color.txt"), "fromfile")
	out := mustRender(t, root, appDoc("fileParameters:\n  - name: color\n    path: params/color.txt\n"))
	if !strings.Contains(out, `color: "fromfile"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRefValuesFileInThisRepo(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "values", "prod.yaml"), "color: fromref\n")
	doc := `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  sources:
    - repoURL: https://example.test/stageproof.git
      path: charts/web
      helm:
        valueFiles:
          - $values/values/prod.yaml
    - repoURL: https://example.test/stageproof
      ref: values
`
	out := mustRender(t, root, doc)
	if !strings.Contains(out, `color: "fromref"`) {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRefToAnotherRepoIsNotChecked(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	doc := `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  sources:
    - repoURL: https://example.test/stageproof.git
      path: charts/web
      helm:
        valueFiles:
          - $values/values/prod.yaml
    - repoURL: https://127.0.0.1:1/other.git
      ref: values
`
	_, err := materialize(t, root, doc)
	var nc *NotCheckedError
	if !errorAs(err, &nc) || !strings.Contains(err.Error(), "does not clone") {
		t.Fatalf("err = %v", err)
	}
}

func TestReleaseNameIsPassedToHelm(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	out := mustRender(t, root, appDoc("releaseName: custom\n"))
	if !strings.Contains(out, "name: custom") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestApplicationsReferencingFindsTheValuesFile(t *testing.T) {
	root := t.TempDir()
	writeColorChart(t, root)
	writeFile(t, filepath.Join(root, "values", "prod.yaml"), "color: fromref\n")
	writeFile(t, filepath.Join(root, "apps", "web.yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  sources:
    - repoURL: https://example.test/stageproof.git
      path: charts/web
      helm:
        valueFiles:
          - $values/values/prod.yaml
    - repoURL: https://example.test/stageproof.git
      ref: values
`)
	got := ApplicationsReferencing(root, []string{"values/prod.yaml"}, nil)
	if len(got) != 1 || !strings.HasSuffix(got[0], filepath.Join("apps", "web.yaml")) {
		t.Fatalf("got %v", got)
	}
}

func parseHelm(t *testing.T, helmYAML string) *AppSpec {
	t.Helper()
	spec, _, err := ParseApplicationBytes([]byte(appDoc(helmYAML)), "app.yaml", nil)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func appDoc(helmYAML string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: demo
spec:
  source:
    repoURL: https://example.test/stageproof.git
    path: charts/web
    helm:
` + indentBlock(helmYAML, "      ")
}

func indentBlock(body, pad string) string {
	body = strings.TrimRight(body, "\n")
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		b.WriteString(pad)
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func writeColorChart(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, "charts", "web")
	writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(dir, "values.yaml"), "color: chart\nsize: chart\n")
	writeFile(t, filepath.Join(dir, "templates", "config.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  color: {{ .Values.color | quote }}
  size: {{ .Values.size | quote }}
  code: {{ .Values.code | default "" | quote }}
`)
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

func materialize(t *testing.T, root, doc string) (*Materialized, error) {
	t.Helper()
	spec, _, err := ParseApplicationBytes([]byte(doc), "app.yaml", nil)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return Materialize(ctx, spec, MaterializeOptions{RepoRoot: root, Remotes: []string{"https://example.test/stageproof.git"}})
}

func mustRender(t *testing.T, root, doc string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	mat, err := materialize(t, root, doc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mat.Cleanup)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := helm.RenderChart(ctx, helm.RenderOptions{
		ReleaseName: mat.ReleaseName,
		ChartDir:    mat.ChartDir,
		ValuesFiles: mat.ValueFiles,
		SetValues:   mat.SetValues,
		SetStrings:  mat.SetStrings,
		SetFiles:    mat.SetFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func errorAs(err error, target **NotCheckedError) bool {
	if err == nil {
		return false
	}
	nc, ok := err.(*NotCheckedError)
	if !ok {
		return false
	}
	*target = nc
	return true
}
