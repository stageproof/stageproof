package report

import (
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func TestFindingOrder(t *testing.T) {
	in := []verdict.Finding{
		{RuleID: "helm.image.changed"},
		{RuleID: "impact.restart"},
		{RuleID: "helm.values.silently_dropped"},
		{RuleID: "impact.exposure.service"},
		{RuleID: "impact.privilege.rbac"},
		{RuleID: "crd.schema.incompatible"},
		{RuleID: "impact.capacity.replicas"},
		{RuleID: "impact.delete.pvc"},
	}
	got := OrderFindings(in)
	var ids []string
	for _, f := range got {
		ids = append(ids, f.RuleID)
	}
	want := []string{
		"impact.delete.pvc",
		"crd.schema.incompatible",
		"impact.privilege.rbac",
		"impact.exposure.service",
		"helm.values.silently_dropped",
		"impact.restart",
		"impact.capacity.replicas",
		"helm.image.changed",
	}
	if len(ids) != len(want) {
		t.Fatal(ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order %v", ids)
		}
	}
}
