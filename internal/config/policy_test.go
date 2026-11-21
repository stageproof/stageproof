package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestIgnoreMarksAFindingAndKeepsIt(t *testing.T) {
	cfg, err := LoadBytes("repo/.stageproof.yaml", []byte(`
ignores:
  - rule: helm.values.silently_dropped
    resource: "config.*"
    reason: "The chart renamed this key."
    expires: "2099-01-01"
`))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Resource: "config.logLevel", Message: "dropped", Detail: "secret-value"},
		{RuleID: "impact.restart", Severity: verdict.SeverityInfo, Resource: "Deployment/default/web", Message: "restart"},
	}}
	Apply(cfg, rep, time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))
	if !rep.Findings[0].Ignored || rep.Findings[0].IgnoreReason == "" || rep.Findings[0].IgnoreExpires != "2099-01-01" {
		t.Fatalf("finding was hidden or not marked: %+v", rep.Findings[0])
	}
	if !strings.Contains(rep.Findings[0].Message, "dropped") || rep.Findings[0].Detail != "secret-value" {
		t.Fatalf("the finding text changed: %+v", rep.Findings[0])
	}
	if rep.Findings[1].Ignored {
		t.Fatalf("a different rule was ignored: %+v", rep.Findings[1])
	}
	rep.Finalize(false, false, false)
	if rep.Verdict != verdict.Passed || rep.ExitCode != 0 {
		t.Fatalf("an ignored blocker still failed the check: %s %d", rep.Verdict, rep.ExitCode)
	}
}

func TestExpiredIgnoreDoesNotMatchAndWarns(t *testing.T) {
	cfg, err := LoadBytes("repo/.stageproof.yaml", []byte(`
ignores:
  - rule: helm.values.silently_dropped
    reason: "old waiver"
    expires: "2026-11-20"
`))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Resource: "size", Message: "dropped"},
	}}
	Apply(cfg, rep, time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	if rep.Findings[0].Ignored {
		t.Fatal("an expired ignore matched")
	}
	if len(rep.Findings) != 2 || rep.Findings[1].RuleID != "config.ignore_expired" || rep.Findings[1].Severity != verdict.SeverityWarning {
		t.Fatalf("findings: %+v", rep.Findings)
	}
	rep.Finalize(false, false, false)
	if rep.Verdict != verdict.Blocked {
		t.Fatalf("the finding must still block, got %s", rep.Verdict)
	}
}

func TestIgnoreOnItsExpiryDateStillMatches(t *testing.T) {
	cfg, err := LoadBytes("x", []byte("ignores:\n  - rule: impact.restart\n    reason: today\n    expires: \"2026-11-21\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "impact.restart", Severity: verdict.SeverityInfo, Message: "restart"},
	}}
	Apply(cfg, rep, time.Date(2026, 11, 21, 23, 0, 0, 0, time.UTC))
	if !rep.Findings[0].Ignored {
		t.Fatal("the expiry date is inclusive")
	}
}

func TestPolicyLongestPrefixWins(t *testing.T) {
	cfg, err := LoadBytes("x", []byte(`
policy:
  impact: warn
  impact.delete: block
  impact.delete.pvc: info
`))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "impact.delete.pvc", Severity: verdict.SeverityBlocker, Message: "pvc"},
		{RuleID: "impact.delete.namespace", Severity: verdict.SeverityWarning, Message: "ns"},
		{RuleID: "impact.restart", Severity: verdict.SeverityInfo, Message: "restart"},
		{RuleID: "helm.image.changed", Severity: verdict.SeverityWarning, Message: "image"},
	}}
	Apply(cfg, rep, time.Now().UTC())
	if rep.Findings[0].Severity != verdict.SeverityInfo || rep.Findings[1].Severity != verdict.SeverityBlocker || rep.Findings[2].Severity != verdict.SeverityWarning || rep.Findings[3].Severity != verdict.SeverityWarning {
		t.Fatalf("severities: %s %s %s %s", rep.Findings[0].Severity, rep.Findings[1].Severity, rep.Findings[2].Severity, rep.Findings[3].Severity)
	}
}

func TestIgnoreRequiresReasonAndDate(t *testing.T) {
	if _, err := LoadBytes("x", []byte("ignores:\n  - rule: impact.restart\n    expires: \"2099-01-01\"\n")); err == nil {
		t.Fatal("a missing reason was accepted")
	}
	if _, err := LoadBytes("x", []byte("ignores:\n  - rule: impact.restart\n    reason: because\n")); err == nil {
		t.Fatal("a missing date was accepted")
	}
	if _, err := LoadBytes("x", []byte("policy:\n  impact.restart: error\n")); err == nil {
		t.Fatal("a bad policy level was accepted")
	}
}

func TestIgnoreRuleIsExact(t *testing.T) {
	cfg, err := LoadBytes("x", []byte("ignores:\n  - rule: helm.values\n    reason: too broad\n    expires: \"2099-01-01\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Message: "dropped"},
	}}
	Apply(cfg, rep, time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	if rep.Findings[0].Ignored {
		t.Fatal("an ignore rule is the id, not a prefix")
	}
}

func TestGlobMatchesAcrossSlashes(t *testing.T) {
	if !globMatch("Deployment/*", "Deployment/default/web") {
		t.Fatal("star should match slashes")
	}
	if globMatch("Service/*", "Deployment/default/web") {
		t.Fatal("a different kind matched")
	}
}

func TestHeadIgnoreIsNotThisPackage(t *testing.T) {
	// The bypass is check-pr loading the base file. Apply only sees the config
	// it is given. A second config must not be consulted.
	base, err := LoadBytes("base", []byte("currentKubernetes: \"1.31\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "helm.values.silently_dropped", Severity: verdict.SeverityBlocker, Message: "dropped"},
	}}
	Apply(base, rep, time.Now().UTC())
	if rep.Findings[0].Ignored {
		t.Fatal("a config with no ignore marked the finding")
	}
}

func TestApplyNewDoesNotRepeatAnExpiredIgnore(t *testing.T) {
	cfg, err := LoadBytes("x", []byte("ignores:\n  - rule: impact.restart\n    reason: old\n    expires: \"2020-01-01\"\npolicy:\n      impact.delete: warn\n"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	rep := &verdict.Report{Findings: []verdict.Finding{
		{RuleID: "impact.delete", Severity: verdict.SeverityBlocker, Message: "gone"},
	}}
	Apply(cfg, rep, now)
	n := len(rep.Findings)
	rep.Findings = append(rep.Findings, verdict.Finding{RuleID: "impact.delete", Severity: verdict.SeverityBlocker, Message: "later"})
	ApplyNew(cfg, rep, n, now)
	expired := 0
	for _, f := range rep.Findings {
		if f.RuleID == "config.ignore_expired" {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("expired warnings: %+v", rep.Findings)
	}
	if rep.Findings[n].Severity != verdict.SeverityWarning {
		t.Fatalf("new finding was not downgraded: %+v", rep.Findings[n])
	}
}
