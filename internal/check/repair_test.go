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

func TestSharedEmptyValueStillRenders(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := chartWithValues(t, "name: \"\"\nenabled: true\n", configMapUsingName())
	head := chartWithValues(t, "name: \"\"\nenabled: true\n", configMapUsingName())
	rep := Run(context.Background(), Input{
		BaseDir: base,
		HeadDir: head,
		Started: time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict %s findings %+v checks %+v", rep.Verdict, rep.Findings, rep.Checks)
	}
	if !checkMentions(rep, "chart defaults", "name") {
		t.Fatalf("missing defaults warning: %+v", rep.Checks)
	}
}

func TestNewRequiredValueStaysBlocked(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := chartWithValues(t, "enabled: true\nname: kept\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  name: {{ .Values.name | quote }}\n")
	head := chartWithValues(t, "enabled: true\nname: \"\"\n", configMapUsingName())
	rep := Run(context.Background(), Input{
		BaseDir: base,
		HeadDir: head,
		Started: time.Now(),
	})
	if rep.Verdict != verdict.Blocked || !rep.HasRule("helm.values.new_required_unset") {
		t.Fatalf("verdict %s findings %+v", rep.Verdict, rep.Findings)
	}
}

func TestSchemaNullIsFilledOnBothCharts(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := schemaChart(t)
	head := schemaChart(t)
	rep := Run(context.Background(), Input{
		BaseDir: base,
		HeadDir: head,
		Started: time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict %s findings %+v checks %+v", rep.Verdict, rep.Findings, rep.Checks)
	}
	// Helm 3 rejects a null string in values.schema.json. Helm 4 renders it.
	// The warning exists only when Helm itself refused the default.
	if helmRenders(t, base) {
		if checkMentions(rep, "chart defaults", "policy") {
			t.Fatalf("filled a value Helm already accepted: %+v", rep.Checks)
		}
		return
	}
	if !checkMentions(rep, "chart defaults", "policy") {
		t.Fatalf("missing schema warning: %+v", rep.Checks)
	}
}

func TestMissingCRDAPIIsAssumed(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	tmpl := "{{- if not (.Capabilities.APIVersions.Has \"example.com/v1alpha1/Widget\") }}\n{{- fail \"example.com/v1alpha1/Widget is not installed\" }}\n{{- end }}\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  ok: \"yes\"\n"
	base := chartWithValues(t, "{}\n", tmpl)
	head := chartWithValues(t, "{}\n", tmpl)
	rep := Run(context.Background(), Input{
		BaseDir: base,
		HeadDir: head,
		Started: time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict %s findings %+v checks %+v", rep.Verdict, rep.Findings, rep.Checks)
	}
	if !checkMentions(rep, "chart defaults", "example.com/v1alpha1/Widget") {
		t.Fatalf("missing API warning: %+v", rep.Checks)
	}
}

func TestUserValueIsNotReplaced(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}
	base := chartWithValues(t, "name: \"\"\nenabled: true\n", configMapUsingName())
	head := chartWithValues(t, "name: \"\"\nenabled: true\n", configMapUsingName())
	values := filepath.Join(t.TempDir(), "values.yaml")
	if err := os.WriteFile(values, []byte("enabled: true\nname: real-name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := Run(context.Background(), Input{
		BaseDir:    base,
		HeadDir:    head,
		BaseValues: []string{values},
		HeadValues: []string{values},
		Started:    time.Now(),
	})
	if rep.Verdict != verdict.Passed {
		t.Fatalf("verdict %s findings %+v checks %+v", rep.Verdict, rep.Findings, rep.Checks)
	}
	if checkMentions(rep, "chart defaults", "stageproof") || checkMentions(rep, "chart defaults", "name") {
		t.Fatalf("user value was replaced: %+v", rep.Checks)
	}
}

func configMapUsingName() string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  name: {{ if .Values.enabled }}{{ required \"name is required in chart values\" .Values.name | quote }}{{ else }}\"none\"{{ end }}\n"
}

func chartWithValues(t *testing.T, values, tmpl string) string {
	t.Helper()
	dir := t.TempDir()
	writeValuesChart(t, dir, values, tmpl)
	return dir
}

func schemaChart(t *testing.T) string {
	t.Helper()
	dir := chartWithValues(t, "policy:\n", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  policy: {{ .Values.policy | quote }}\n")
	schema := `{
  "type": "object",
  "properties": {"policy": {"type": "string"}}
}`
	if err := os.WriteFile(filepath.Join(dir, "values.schema.json"), []byte(schema), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func helmRenders(t *testing.T, dir string) bool {
	t.Helper()
	cmd := exec.Command("helm", "template", "rel", dir, "--namespace", "stageproof-check")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("helm template refused the chart: %s", out)
		return false
	}
	return true
}

func checkMentions(rep *verdict.Report, name, text string) bool {
	for _, check := range rep.Checks {
		if check.Name == name && strings.Contains(check.Summary, text) {
			return true
		}
	}
	return false
}
