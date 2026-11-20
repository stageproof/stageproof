package k8sschema

import (
	"testing"

	"github.com/stageproof/stageproof/internal/verdict"
)

func rulesFired(objs []ManifestObject, current, next string) map[string]verdict.Severity {
	out := map[string]verdict.Severity{}
	for _, f := range CheckCompatibility(objs, current, next, true) {
		out[f.RuleID] = f.Severity
	}
	return out
}

// A kind that joined an old group/version later must be dated by its own rule,
// not by the wildcard that covers the kinds removed from that version earlier.
func TestSpecificKindRuleBeatsWildcard(t *testing.T) {
	for _, kind := range []string{"ValidatingAdmissionPolicy", "ValidatingAdmissionPolicyBinding"} {
		obj := []ManifestObject{{APIVersion: "admissionregistration.k8s.io/v1beta1", Kind: kind, Metadata: ObjectMetadata{Name: "demo"}}}

		on133 := rulesFired(obj, "1.33", "")
		if _, removed := on133["k8s.api.removed"]; removed {
			t.Fatalf("%s v1beta1 is still served on 1.33 and must not be reported as removed: %v", kind, on133)
		}
		if on133["k8s.api.deprecated"] != verdict.SeverityWarning {
			t.Fatalf("%s v1beta1 on 1.33 should be a deprecation warning, got %v", kind, on133)
		}

		if rulesFired(obj, "1.33", "1.34")["k8s.api.eol_blocker"] != verdict.SeverityWarning {
			t.Fatalf("%s v1beta1 should warn before a cluster upgrade to 1.34", kind)
		}
		if rulesFired(obj, "1.34", "")["k8s.api.removed"] != verdict.SeverityBlocker {
			t.Fatalf("%s v1beta1 is removed in 1.34", kind)
		}
	}
}

func TestIdentityKeepsThePrintedFormAndSeparatesGroups(t *testing.T) {
	core := ManifestObject{APIVersion: "v1", Kind: "ConfigMap", Metadata: ObjectMetadata{Name: "app"}}
	if core.Group() != "" || core.ID() != "ConfigMap/default/app" {
		t.Fatalf("core identity = %s %s", core.Group(), core.ID())
	}
	a := ManifestObject{APIVersion: "apps/v1", Kind: "Deployment", Metadata: ObjectMetadata{Namespace: "demo", Name: "web"}}
	b := ManifestObject{APIVersion: "example.com/v1", Kind: "Deployment", Metadata: ObjectMetadata{Namespace: "demo", Name: "web"}}
	if a.ID() != "Deployment/demo/web" || b.ID() != "Deployment/demo/web" {
		t.Fatalf("printed form changed: %s %s", a.ID(), b.ID())
	}
	if a.matchKey() == b.matchKey() {
		t.Fatal("different API groups must not share an identity")
	}
}

func TestParseKeepsTheWholeDocument(t *testing.T) {
	raw := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\ndata:\n  file: hello\n"
	objs, err := ParseManifestStream(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 1 {
		t.Fatalf("objects: %d", len(objs))
	}
	data, _ := objs[0].Object["data"].(map[string]any)
	if data["file"] != "hello" {
		t.Fatalf("data outside spec was dropped: %#v", objs[0].Object)
	}
}

func TestWildcardStillCoversOtherKinds(t *testing.T) {
	obj := []ManifestObject{{APIVersion: "admissionregistration.k8s.io/v1beta1", Kind: "ValidatingWebhookConfiguration", Metadata: ObjectMetadata{Name: "hook"}}}
	if rulesFired(obj, "1.33", "")["k8s.api.removed"] != verdict.SeverityBlocker {
		t.Fatalf("ValidatingWebhookConfiguration v1beta1 was removed in 1.22 and must block on 1.33")
	}

	flow := []ManifestObject{{APIVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "FlowSchema", Metadata: ObjectMetadata{Name: "fs"}}}
	if rulesFired(flow, "1.32", "")["k8s.api.removed"] != verdict.SeverityBlocker {
		t.Fatalf("FlowSchema v1beta3 was removed in 1.32")
	}
	if _, removed := rulesFired(flow, "1.31", "")["k8s.api.removed"]; removed {
		t.Fatalf("FlowSchema v1beta3 is still served on 1.31")
	}
}
