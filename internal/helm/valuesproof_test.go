package helm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestDeadAssignmentIsUnreadUntilUsed(t *testing.T) {
	dir := t.TempDir()
	writeChart(t, dir, "unused: hello\n", "{{- $x := .Values.unused -}}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	usage, err := AnalyzeChartUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Decision([]string{"unused"}) != "exact" {
		t.Fatalf("listed key decision = %s", usage.Decision([]string{"unused"}))
	}
	// A chart that drops the key and only assigns it is unread.
	if err := os.Remove(filepath.Join(dir, "values.yaml")); err != nil {
		t.Fatal(err)
	}
	usage, err = AnalyzeChartUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Decision([]string{"unused"}) != "unread" {
		t.Fatalf("dead assignment decision = %s", usage.Decision([]string{"unused"}))
	}
	writeChart(t, dir, "", "{{- $x := .Values.unused -}}{{ $x }}\n")
	usage, err = AnalyzeChartUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Decision([]string{"unused"}) != "exact" {
		t.Fatalf("used variable decision = %s", usage.Decision([]string{"unused"}))
	}
}

func TestListIsOneLeaf(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(path, []byte("hosts:\n  - one.example\n  - two.example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	leaves, _, err := ExtractUserValueLeaves(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaves) != 1 || strings.Join(leaves[0], ".") != "hosts" {
		t.Fatalf("leaves = %#v", leaves)
	}
}

func TestRenderProofFixtures(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	secret := "user-secret-token"

	t.Run("dead assignment is dropped", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "unused: hello\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: {{ .Values.unused | quote }}\n")
		writeChart(t, head, "replicas: 1\n", "{{- $x := .Values.unused -}}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: \"ok\"\n")
		findings := prove(t, base, head, "unused: "+secret+"\n")
		f := mustRule(t, findings, "helm.values.silently_dropped")
		if f.Proof != "render" {
			t.Fatalf("proof = %s", f.Proof)
		}
		assertNoSecret(t, findings, secret)
	})

	t.Run("used variable is not dropped", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		body := "{{- $x := .Values.unused -}}apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: {{ $x | quote }}\n"
		writeChart(t, base, "unused: hello\n", body)
		writeChart(t, head, "replicas: 1\n", body)
		findings := prove(t, base, head, "unused: "+secret+"\n")
		if hasRule(findings, "helm.values.silently_dropped") {
			t.Fatalf("a used variable was dropped: %+v", findings)
		}
	})

	t.Run("value equal to the default is not dropped", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		body := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: demo\nspec:\n  replicas: {{ .Values.replicas }}\n  selector:\n    matchLabels:\n      app: demo\n  template:\n    metadata:\n      labels:\n        app: demo\n    spec:\n      containers:\n        - name: app\n          image: nginx:1\n"
		writeChart(t, base, "replicas: 1\n", body)
		writeChart(t, head, "replicas: 2\n", body)
		findings := proveFull(t, base, head, "replicas: 2\n")
		if hasRule(findings, "helm.values.silently_dropped") {
			t.Fatalf("default-valued key was dropped: %+v", findings)
		}
	})

	t.Run("schema rejection is not a block", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "color: blue\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  color: {{ .Values.color | quote }}\n")
		writeChart(t, head, "replicas: 1\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  color: \"blue\"\n")
		writeFile(t, filepath.Join(head, "values.schema.json"), `{
  "type": "object",
  "properties": {"color": {"type": "string", "enum": ["blue", "green"]}}
}`)
		findings := prove(t, base, head, "color: blue\n")
		if hasRule(findings, "helm.values.silently_dropped") {
			t.Fatalf("schema rejection blocked: %+v", findings)
		}
	})

	t.Run("dropped key without a schema is blocked", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "color: blue\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  color: {{ .Values.color | quote }}\n")
		writeChart(t, head, "replicas: 1\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  color: \"blue\"\n")
		findings := prove(t, base, head, "color: blue\n")
		f := mustRule(t, findings, "helm.values.silently_dropped")
		if f.Proof != "render" {
			t.Fatalf("proof = %s", f.Proof)
		}
	})

	t.Run("volatile env does not hide a dropped tag", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		deploy := func(mode string) string {
			return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
    spec:
      containers:
        - name: app
          image: nginx:{{ .Values.tag }}
          env:
            - name: TOKEN
              value: {{ randAlpha 8 | quote }}
            - name: MODE
              value: ` + mode + `
`
		}
		writeChart(t, base, "tag: \"1.0\"\nmode: quiet\n", deploy(`{{ .Values.mode | quote }}`))
		writeChart(t, head, "tag: \"1.0\"\nmode: quiet\n", deploy(`"fixed"`))
		findings := proveFull(t, base, head, "tag: \"1.2.3\"\nmode: loud\n")
		f := mustRule(t, findings, "helm.values.silently_dropped")
		if f.Resource != "mode" || f.Proof != "render" {
			t.Fatalf("proof = %+v findings %+v", f, findings)
		}
	})

	t.Run("verified rename", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "service:\n  mode: hello\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: {{ .Values.service.mode | quote }}\n")
		writeChart(t, head, "extra:\n  mode: hello\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: {{ .Values.extra.mode | quote }}\n")
		findings := prove(t, base, head, "service:\n  mode: "+secret+"\n")
		f := mustRule(t, findings, "helm.values.renamed")
		if f.Verified == nil || !*f.Verified {
			t.Fatalf("rename was not verified: %+v", f)
		}
		if !strings.Contains(f.Message, "extra.mode") || strings.Contains(f.Message, secret) {
			t.Fatalf("message = %s", f.Message)
		}
		assertNoSecret(t, findings, secret)
	})

	t.Run("similar name with a different effect is not verified", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "service:\n  mode: hello\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: {{ .Values.service.mode | quote }}\n")
		writeChart(t, head, "extra:\n  mode: hello\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  msg: \"fixed\"\n  image: {{ .Values.extra.mode | quote }}\n")
		findings := prove(t, base, head, "service:\n  mode: "+secret+"\n")
		f := mustRule(t, findings, "helm.values.renamed")
		if f.Verified == nil || *f.Verified {
			t.Fatalf("different effect was verified: %+v", f)
		}
		assertNoSecret(t, findings, secret)
	})

	t.Run("yaml list is one key", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		writeChart(t, base, "hosts: []\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  hosts: {{ join \",\" .Values.hosts | quote }}\n")
		writeChart(t, head, "replicas: 1\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  hosts: \"none\"\n")
		findings := prove(t, base, head, "hosts:\n  - one.example\n  - two.example\n")
		f := mustRule(t, findings, "helm.values.silently_dropped")
		if f.Resource != "hosts" {
			t.Fatalf("resource = %s", f.Resource)
		}
		for _, other := range findings {
			if strings.Contains(other.Resource, "hosts[") {
				t.Fatalf("list element was its own key: %+v", other)
			}
		}
	})

	t.Run("changed default", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		body := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: demo\nspec:\n  replicas: {{ .Values.replicas }}\n  selector:\n    matchLabels:\n      app: demo\n  template:\n    metadata:\n      labels:\n        app: demo\n    spec:\n      containers:\n        - name: app\n          image: {{ .Values.image | quote }}\n"
		writeChart(t, base, "replicas: 1\nimage: nginx:1\n", body)
		writeChart(t, head, "replicas: 2\nimage: nginx:1\n", body)
		findings := prove(t, base, head, "image: "+secret+"\n")
		f := mustRule(t, findings, "helm.values.default_changed")
		if f.Resource != "replicas" || !strings.Contains(f.Message, "1") || !strings.Contains(f.Message, "2") {
			t.Fatalf("default finding = %+v", f)
		}
		assertNoSecret(t, findings, secret)
	})

	t.Run("random output stays stable", func(t *testing.T) {
		base := t.TempDir()
		head := t.TempDir()
		baseBody := `
apiVersion: v1
kind: Secret
metadata:
  name: tok
data:
  token: {{ randAlphaNum 8 | b64enc }}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo
  template:
    metadata:
      labels:
        app: demo
      annotations:
        checksum/secret: {{ randAlphaNum 8 | quote }}
    spec:
      containers:
        - name: app
          image: nginx:{{ .Values.tag }}
          args: ["{{ .Values.token }}"]
`
		headBody := strings.Replace(baseBody, `args: ["{{ .Values.token }}"]`, `args: ["fixed"]`, 1)
		writeChart(t, base, "tag: \"1\"\ntoken: hello\n", baseBody)
		writeChart(t, head, "tag: \"1\"\n", headBody)
		var first []verdict.Finding
		for i := 0; i < 5; i++ {
			got := stripFiles(prove(t, base, head, "tag: \"1\"\ntoken: "+secret+"\n"))
			if i == 0 {
				first = got
				if !hasRule(got, "helm.values.silently_dropped") {
					t.Fatalf("random chart hid the dropped key: %+v", got)
				}
				continue
			}
			if !reflect.DeepEqual(got, first) {
				t.Fatalf("run %d differed\nfirst %+v\nnow %+v", i, first, got)
			}
		}
		assertNoSecret(t, first, secret)
	})
}

func TestProveValuesIsSortedAcrossRuns(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := t.TempDir()
	head := t.TempDir()
	writeChart(t, base, "a: one\nb: two\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  a: {{ .Values.a | quote }}\n  b: {{ .Values.b | quote }}\n")
	writeChart(t, head, "a: one\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  a: {{ .Values.a | quote }}\n")
	first := stripFiles(prove(t, base, head, "a: one\nb: two\n"))
	second := stripFiles(prove(t, base, head, "a: one\nb: two\n"))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("findings were not stable\n%+v\n%+v", first, second)
	}
}

func prove(t *testing.T, base, head, values string) []verdict.Finding {
	t.Helper()
	return proveMode(t, base, head, values, false)
}

func proveFull(t *testing.T, base, head, values string) []verdict.Finding {
	t.Helper()
	return proveMode(t, base, head, values, true)
}

func proveMode(t *testing.T, base, head, values string, full bool) []verdict.Finding {
	t.Helper()
	path := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(path, []byte(values), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, _, err := ProveValues(context.Background(), ProofRequest{
		BaseDir:     base,
		HeadDir:     head,
		ValuesFiles: []string{path},
		Base:        RenderOptions{ChartDir: base, ValuesFiles: []string{path}},
		Head:        RenderOptions{ChartDir: head, ValuesFiles: []string{path}},
		Full:        full,
	})
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func stripFiles(in []verdict.Finding) []verdict.Finding {
	out := append([]verdict.Finding(nil), in...)
	for i := range out {
		out[i].File = ""
	}
	return out
}

func writeChart(t *testing.T, dir, values, tmpl string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	if values != "" {
		writeFile(t, filepath.Join(dir, "values.yaml"), values)
	}
	writeFile(t, filepath.Join(dir, "templates/obj.yaml"), tmpl)
}

func mustRule(t *testing.T, findings []verdict.Finding, id string) verdict.Finding {
	t.Helper()
	for _, f := range findings {
		if f.RuleID == id {
			return f
		}
	}
	t.Fatalf("missing %s in %+v", id, findings)
	return verdict.Finding{}
}

func hasRule(findings []verdict.Finding, id string) bool {
	for _, f := range findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func assertNoSecret(t *testing.T, findings []verdict.Finding, secret string) {
	t.Helper()
	for _, f := range findings {
		if strings.Contains(f.Message, secret) || strings.Contains(f.Detail, secret) || strings.Contains(f.Resource, secret) {
			t.Fatalf("finding printed a user value: %+v", f)
		}
	}
}
