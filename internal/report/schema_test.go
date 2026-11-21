package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stageproof/stageproof/internal/verdict"
)

func TestGoldenJSONMatchesReportSchema(t *testing.T) {
	root := moduleRoot(t)
	sch := compileSchema(t, filepath.Join(root, "docs", "report.schema.json"))
	dir := filepath.Join(root, "testdata", "golden")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		n++
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := validateJSON(sch, raw); err != nil {
			t.Errorf("%s: %v", entry.Name(), err)
		}
	}
	if n == 0 {
		t.Fatal("no golden JSON files")
	}
}

func TestSARIFMatchesOfficialSchema(t *testing.T) {
	verified := true
	reports := []*verdict.Report{
		{
			Verdict:  verdict.Blocked,
			ExitCode: 1,
			BaseRef:  "base",
			HeadRef:  "head",
			Findings: []verdict.Finding{
				{
					RuleID:   "helm.values.silently_dropped",
					Severity: verdict.SeverityBlocker,
					File:     "values file.yaml",
					Resource: "size",
					Message:  "Values key `size` was consumed in baseline chart but is silently ignored in upgraded chart.",
					Detail:   "secret-value",
					Proof:    "render",
					Verified: &verified,
				},
				{
					RuleID:   "impact.restart",
					Severity: verdict.SeverityInfo,
					Resource: "Deployment/default/web",
					Message:  "The pods will restart.",
				},
			},
			Skipped: []verdict.SkipNotice{{
				Code:        "rehearsal_required",
				Reason:      "The upgrade was not rehearsed.",
				Remediation: "Re-run with --live.",
			}},
		},
		{
			Verdict:  verdict.Passed,
			ExitCode: 0,
			BaseRef:  "other",
			HeadRef:  "other-head",
			Findings: []verdict.Finding{{
				RuleID:   "impact.exposure.service",
				Severity: verdict.SeverityWarning,
				Message:  "Service/default/web is a LoadBalancer.",
			}},
		},
	}
	var buf strings.Builder
	if err := WriteSARIF(&buf, reports, "dev", string(verdict.Blocked), 1); err != nil {
		t.Fatal(err)
	}
	raw := buf.String()
	if strings.Contains(raw, "secret-value") {
		t.Fatalf("SARIF included finding detail:\n%s", raw)
	}
	if !strings.Contains(raw, `"version": "2.1.0"`) || !strings.Contains(raw, `"ruleId": "helm.values.silently_dropped"`) {
		t.Fatalf("SARIF:\n%s", raw)
	}
	if !strings.Contains(raw, `"level": "error"`) || !strings.Contains(raw, `"level": "note"`) || !strings.Contains(raw, "rehearsal_required") {
		t.Fatalf("levels or skip missing:\n%s", raw)
	}
	if !strings.Contains(raw, "values%20file.yaml") {
		t.Fatalf("file uri was not escaped:\n%s", raw)
	}
	root := moduleRoot(t)
	sch := compileSchema(t, filepath.Join(root, "docs", "sarif-schema-2.1.0.json"))
	if err := validateJSON(sch, []byte(raw)); err != nil {
		t.Fatalf("official SARIF schema: %v\n%s", err, raw)
	}

	var again strings.Builder
	if err := WriteSARIF(&again, reports, "dev", string(verdict.Blocked), 1); err != nil {
		t.Fatal(err)
	}
	if again.String() != raw {
		t.Fatal("SARIF output is not stable")
	}
}

func TestIgnoredFindingStaysInTheSchema(t *testing.T) {
	rep := &verdict.Report{
		SchemaVersion: 1,
		Verdict:       verdict.Passed,
		ExitCode:      0,
		Adapter:       "helm",
		TargetK8s:     "1.33",
		BaseRef:       "base",
		HeadRef:       "head",
		Checked:       "files",
		Timestamp:     time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC),
		Checks:        []verdict.CheckResult{{Name: "values", Status: "fail", Summary: "1 key"}},
		Findings: []verdict.Finding{{
			RuleID:        "helm.values.silently_dropped",
			Severity:      verdict.SeverityBlocker,
			Message:       "dropped",
			Detail:        "secret-value",
			Ignored:       true,
			IgnoreReason:  "The chart renamed this key.",
			IgnoreExpires: "2099-01-01",
		}},
	}
	var buf strings.Builder
	if err := WriteJSON(&buf, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "secret-value") || !strings.Contains(buf.String(), `"ignored": true`) {
		t.Fatalf("json:\n%s", buf.String())
	}
	root := moduleRoot(t)
	sch := compileSchema(t, filepath.Join(root, "docs", "report.schema.json"))
	if err := validateJSON(sch, []byte(buf.String())); err != nil {
		t.Fatal(err)
	}
	var sarif strings.Builder
	if err := WriteSARIF(&sarif, []*verdict.Report{rep}, "dev", "PASSED", 0); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sarif.String(), "helm.values.silently_dropped") || !strings.Contains(sarif.String(), "suppressions") || strings.Contains(sarif.String(), "secret-value") {
		t.Fatalf("sarif:\n%s", sarif.String())
	}
	sarifSchema := compileSchema(t, filepath.Join(root, "docs", "sarif-schema-2.1.0.json"))
	if err := validateJSON(sarifSchema, []byte(sarif.String())); err != nil {
		t.Fatal(err)
	}
}

func TestSARIFEmptyReportIsValid(t *testing.T) {
	var buf strings.Builder
	if err := WriteSARIF(&buf, []*verdict.Report{{Verdict: verdict.Passed, ExitCode: 0}}, "dev", "PASSED", 0); err != nil {
		t.Fatal(err)
	}
	root := moduleRoot(t)
	sch := compileSchema(t, filepath.Join(root, "docs", "sarif-schema-2.1.0.json"))
	if err := validateJSON(sch, []byte(buf.String())); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"results": null`) {
		t.Fatalf("empty results must be an array:\n%s", buf.String())
	}
}

func compileSchema(t *testing.T, path string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	sch, err := c.Compile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sch
}

func validateJSON(sch *jsonschema.Schema, raw []byte) error {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	return sch.Validate(doc)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
