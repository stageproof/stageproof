package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestBriefOmitsEmptySectionsAndKeepsOrder(t *testing.T) {
	r := &verdict.Report{
		Findings: []verdict.Finding{
			{RuleID: "helm.image.changed", Severity: verdict.SeverityWarning, Message: "image changed"},
			{RuleID: "impact.capacity.replicas", Severity: verdict.SeverityInfo, Message: "replicas down"},
			{RuleID: "impact.restart", Severity: verdict.SeverityInfo, Message: "pods restart"},
			{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Message: "key `size` dropped", Detail: "secret-value"},
			{RuleID: "impact.exposure.service", Severity: verdict.SeverityWarning, Message: "Service/default/web is a LoadBalancer"},
			{RuleID: "impact.privilege.rbac", Severity: verdict.SeverityWarning, Message: "Role/default/web adds rules"},
			{RuleID: "crd.schema.incompatible", Severity: verdict.SeverityWarning, Message: "field removed"},
			{RuleID: "impact.delete.pvc", Severity: verdict.SeverityBlocker, Message: "Helm will delete PersistentVolumeClaim/default/data."},
			{RuleID: "k8s.api.removed", Severity: verdict.SeverityBlocker, Message: "removed api"},
		},
		Skipped: []verdict.SkipNotice{{
			Code:   "missing_external_crds",
			Reason: "Chart renders a custom resource whose CRD is not available.",
		}},
	}
	r.Findings = OrderFindings(r.Findings)

	var term bytes.Buffer
	writeBrief(&term, r, false)
	got := term.String()
	want := strings.Join([]string{
		"Deletes",
		"  ERROR  Helm will delete PersistentVolumeClaim/default/data.",
		"",
		"CRDs",
		"  WARN  field removed",
		"",
		"Privilege",
		"  WARN  Role/default/web adds rules",
		"",
		"Exposure",
		"  WARN  Service/default/web is a LoadBalancer",
		"",
		"Values",
		"  ERROR  key `size` dropped",
		"",
		"Restarts",
		"  INFO  pods restart",
		"",
		"Not checked",
		"  missing_external_crds  Chart renders a custom resource whose CRD is not available.",
		"",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("brief:\n%s", got)
	}
	if strings.Contains(got, "secret-value") || strings.Contains(got, "replicas") || strings.Contains(got, "image changed") || strings.Contains(got, "removed api") {
		t.Fatalf("brief included a line that is not one of the seven sections:\n%s", got)
	}

	var md bytes.Buffer
	writeBriefMarkdown(&md, r)
	mdGot := md.String()
	if !strings.Contains(mdGot, "### Deletes\n\n- **ERROR** Helm will delete") {
		t.Fatalf("markdown brief:\n%s", mdGot)
	}
	if strings.Contains(mdGot, "### Capacity") || strings.Contains(mdGot, "secret-value") {
		t.Fatalf("markdown brief leaked a section or a detail:\n%s", mdGot)
	}
	deletes := strings.Index(mdGot, "### Deletes")
	values := strings.Index(mdGot, "### Values")
	notChecked := strings.Index(mdGot, "### Not checked")
	if deletes < 0 || values < deletes || notChecked < values {
		t.Fatalf("markdown order:\n%s", mdGot)
	}
}

func TestBriefIsAbsentWhenNothingMatches(t *testing.T) {
	r := &verdict.Report{
		Findings: []verdict.Finding{
			{RuleID: "helm.image.changed", Severity: verdict.SeverityWarning, Message: "image changed"},
			{RuleID: "impact.capacity.pdb", Severity: verdict.SeverityInfo, Message: "budget changed"},
		},
	}
	var buf bytes.Buffer
	writeBrief(&buf, r, false)
	writeBriefMarkdown(&buf, r)
	if buf.Len() != 0 {
		t.Fatalf("empty brief must print nothing:\n%s", buf.String())
	}
}

func TestBriefSitsAboveTheCheckList(t *testing.T) {
	var term bytes.Buffer
	writeTerminal(&term, sampleReport(), false)
	out := term.String()
	values := strings.Index(out, "\nValues\n")
	checks := strings.Index(out, "  ✓ PASS  helm template")
	if values < 0 || checks < 0 || values > checks {
		t.Fatalf("the brief must come before the check list:\n%s", out)
	}
	if strings.Contains(out, "secret") {
		t.Fatalf("sample brief leaked a value:\n%s", out)
	}
	if !strings.Contains(out, "\nNot checked\n  missing_external_crds  ") {
		t.Fatalf("skip notices belong in Not checked:\n%s", out)
	}
	if !strings.Contains(out, "0 AI calls") {
		t.Fatalf("the brief does not replace the AI line:\n%s", out)
	}

	var md bytes.Buffer
	WriteMarkdown(&md, sampleReport())
	mdOut := md.String()
	if strings.Index(mdOut, "### Values") > strings.Index(mdOut, "| Check |") {
		t.Fatalf("markdown brief must come before the check table:\n%s", mdOut)
	}
}
