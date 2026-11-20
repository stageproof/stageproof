package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func sampleReport() *verdict.Report {
	return &verdict.Report{
		Verdict:    verdict.Passed,
		ExitCode:   0,
		Adapter:    "helm",
		TargetK8s:  "1.35",
		NextK8sEOL: "1.36",
		BaseRef:    "argo-cd@10.9.6",
		HeadRef:    "argo-cd@10.18.0",
		Checked:    verdict.CheckedFiles,
		DurationMs: 2829,
		Checks: []verdict.CheckResult{
			{Name: "helm template", Status: "pass", Summary: "rendered 51 Kubernetes objects cleanly"},
			{Name: "values.yaml AST", Status: "warn", Summary: "one key renamed"},
		},
		Findings: []verdict.Finding{
			{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Message: "key dropped"},
			{RuleID: "k8s.api.deprecated", Severity: verdict.SeverityWarning, Message: "api deprecated"},
		},
		Skipped: []verdict.DegradedNotice{{
			Code:        "missing_external_crds",
			Reason:      "Chart renders a custom resource whose CRD is not available.",
			Remediation: "Pass --crd-file and re-run with --live.",
		}},
	}
}

func TestFileCheckSaysItWasNotTestedLive(t *testing.T) {
	var buf bytes.Buffer
	writeTerminal(&buf, sampleReport(), true)
	out := buf.String()
	if !strings.Contains(out, ansiRed+"NOT TESTED LIVE") {
		t.Fatalf("a file check must say so in red:\n%s", out)
	}
	if !strings.Contains(out, ansiGreen+"PASSED") {
		t.Fatalf("the passing verdict stays green:\n%s", out)
	}
	var md bytes.Buffer
	WriteMarkdown(&md, sampleReport())
	if !strings.Contains(md.String(), "> NOT TESTED LIVE. This job did not rehearse the upgrade.") {
		t.Fatalf("the comment must carry the same sentence:\n%s", md.String())
	}
}

func TestAPIServerCheckStillSaysNotTestedLive(t *testing.T) {
	rep := sampleReport()
	rep.Findings = nil
	rep.Skipped = nil
	rep.Checked = "files, API server 1.35.0"
	var buf bytes.Buffer
	writeTerminal(&buf, rep, true)
	if !strings.Contains(buf.String(), "NOT TESTED LIVE") {
		t.Fatalf("an API server proof is not a rehearsal:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "checked: files, API server 1.35.0") {
		t.Fatalf("the patch that ran must be named:\n%s", buf.String())
	}
}

func TestTerminalColors(t *testing.T) {
	var buf bytes.Buffer
	writeTerminal(&buf, sampleReport(), true)
	out := buf.String()
	if !strings.Contains(out, "missing_external_crds") {
		t.Fatalf("missing skip notice:\n%s", out)
	}
	if !strings.Contains(out, "checked: files") {
		t.Fatalf("missing checked scope:\n%s", out)
	}
	if !strings.Contains(out, ansiYellow+"WARN") || !strings.Contains(out, ansiRed+"ERROR") {
		t.Fatalf("warn must be yellow and errors red:\n%s", out)
	}
	if !strings.Contains(out, ansiGreen+"PASS") {
		t.Fatalf("pass must be green:\n%s", out)
	}
	if strings.Contains(out, "**") || strings.Contains(out, "| Check |") {
		t.Fatalf("job log must not be markdown:\n%s", out)
	}
}

// Two checks of one chart differ only in the values file, so the report names it.
func TestTerminalNamesTheValuesFiles(t *testing.T) {
	rep := sampleReport()
	rep.ValuesFiles = []string{"charts/api/values-prod.yaml"}
	var buf bytes.Buffer
	writeTerminal(&buf, rep, false)
	if !strings.Contains(buf.String(), "\nvalues  charts/api/values-prod.yaml\n") {
		t.Fatalf("report must name the values file:\n%s", buf.String())
	}

	rep.ValuesFiles = nil
	buf.Reset()
	writeTerminal(&buf, rep, false)
	if strings.Contains(buf.String(), "\nvalues") {
		t.Fatalf("no values line when the chart defaults are checked:\n%s", buf.String())
	}
}

func TestCombinedMarkdownForOneReportIsThatReport(t *testing.T) {
	var single, combined bytes.Buffer
	WriteMarkdown(&single, sampleReport())
	WriteCombinedMarkdown(&combined, &Combined{Verdict: verdict.Passed, ExitCode: 0, Reports: []*verdict.Report{sampleReport()}})
	if single.String() != combined.String() {
		t.Fatalf("one chart must give the same comment as before:\n%s", combined.String())
	}
}

func TestCombinedMarkdownListsEveryReport(t *testing.T) {
	blocked := sampleReport()
	blocked.Verdict, blocked.ExitCode = verdict.Blocked, 1
	blocked.BaseRef, blocked.HeadRef = "main:charts/api", "charts/api"
	blocked.ValuesFiles = []string{"charts/api/values-prod.yaml"}

	var buf bytes.Buffer
	WriteCombinedMarkdown(&buf, &Combined{Verdict: verdict.Blocked, ExitCode: 1, Reports: []*verdict.Report{sampleReport(), blocked}})
	out := buf.String()

	if !strings.HasPrefix(out, "## Stageproof: `BLOCKED` (exit `1`) across 2 checks") {
		t.Fatalf("summary must lead with the worst result:\n%s", out)
	}
	if !strings.Contains(out, "| `main:charts/api` → `charts/api` | `charts/api/values-prod.yaml` | **BLOCKED** |") {
		t.Fatalf("summary table must name the blocked chart and its values file:\n%s", out)
	}
	if !strings.Contains(out, "| `argo-cd@10.9.6` → `argo-cd@10.18.0` | chart defaults | **PASSED** |") {
		t.Fatalf("summary table must list the passing chart:\n%s", out)
	}
	if strings.Count(out, "## Stageproof Upgrade Gate:") != 2 {
		t.Fatalf("every report must follow the summary in full:\n%s", out)
	}
}

func TestTerminalPlainHasNoANSI(t *testing.T) {
	var buf bytes.Buffer
	writeTerminal(&buf, sampleReport(), false)
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Fatalf("plain log leaked color:\n%s", out)
	}
	if !strings.Contains(out, "  ! WARN  missing_external_crds") || strings.Contains(out, "!  WARN") {
		t.Fatalf("warning mark must use one space, same as the pass mark:\n%s", out)
	}
}
