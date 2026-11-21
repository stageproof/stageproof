package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestExplainPrintsOneRuleAndRejectsAnUnknownID(t *testing.T) {
	var out, errb bytes.Buffer
	if code := explainTo(&out, &errb, []string{"helm.values.silently_dropped"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "helm.values.silently_dropped") || !strings.Contains(out.String(), "ERROR") || !strings.Contains(out.String(), "What to do") {
		t.Fatalf("output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "%%") {
		t.Fatalf("markup leaked:\n%s", out.String())
	}
	out.Reset()
	errb.Reset()
	if code := explainTo(&out, &errb, []string{"not.a.rule"}); code != 2 || out.Len() != 0 {
		t.Fatalf("unknown id exit=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := explainTo(&out, &errb, nil); code != 2 {
		t.Fatalf("missing id exit=%d", code)
	}
}
