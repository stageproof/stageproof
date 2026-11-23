package helm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProposeRepairReadsSchemaNull(t *testing.T) {
	err := errString("Error: values don't meet the specifications of the schema(s) in the following chart(s):\nweb:\n- at '/policy': got null, want string")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if len(repair.SetStrings) != 1 || repair.SetStrings[0] != "policy="+repairPlaceholder {
		t.Fatalf("schema repair = %#v", repair)
	}
}

func TestProposeRepairReadsDigPath(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "templates/obj.yaml", `{{- $bucket := required "Please define loki.storage.bucketNames.chunks" (dig "storage" "bucketNames" "chunks" "" .Values.loki) }}`)
	err := errString("Error: execution error at (loki/templates/obj.yaml:1:4): Please define loki.storage.bucketNames.chunks")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "loki.storage.bucketNames.chunks="+repairPlaceholder) {
		t.Fatalf("dig repair = %#v", repair)
	}
}

func TestProposeRepairPrefixesSubchart(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "Chart.yaml", "apiVersion: v2\nname: parent\nversion: 0.1.0\ndependencies:\n  - name: certmanager-issuer\n    alias: certs\n")
	writeRepairFile(t, dir, "charts/certmanager-issuer/templates/issuer.yaml", `email: {{ required "You must provide an email to associate with your TLS certificates. Please set certmanager-issuer.email" .Values.email | quote }}`)
	err := errString("Error: execution error at (parent/charts/certmanager-issuer/templates/issuer.yaml:1:10): You must provide an email to associate with your TLS certificates. Please set certmanager-issuer.email")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "certs.email="+repairPlaceholder) {
		t.Fatalf("subchart repair = %#v", repair)
	}
}

func TestProposeRepairFollowsCopiedValues(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "templates/config.tpl", `
{{- $auth := mustMergeOverwrite (mustDeepCopy .Values) .Values.auth -}}
cluster_name: {{ required "clusterName is required in chart values" $auth.clusterName }}
`)
	err := errString("Error: execution error at (teleport/templates/config.tpl:3:16): clusterName is required in chart values")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "auth.clusterName="+repairPlaceholder) || !containsAssignment(repair.SetStrings, "clusterName="+repairPlaceholder) {
		t.Fatalf("variable repair = %#v", repair)
	}
}

func TestProposeRepairUsesTemplateChoice(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "templates/notes.txt", `
{{- if not (eq .Values.mode "daemonset") }}
{{- fail "mode must be set for this chart" }}
{{- end }}
`)
	err := errString("Error: execution error at (web/templates/notes.txt:2:4): mode must be set for this chart")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "mode=daemonset") {
		t.Fatalf("choice repair = %#v", repair)
	}
}

func TestProposeRepairKeepsOneOfTwoValues(t *testing.T) {
	err := errString("Error: execution error at (web/templates/secret.yaml:2:4): Either set .Values.zitadel.masterkey xor .Values.zitadel.masterkeySecretName")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "zitadel.masterkey="+repairPlaceholder) {
		t.Fatalf("kept value missing: %#v", repair)
	}
	for _, assignment := range append(repair.SetStrings, repair.Sets...) {
		if strings.HasPrefix(assignment, "zitadel.masterkeySecretName=") {
			t.Fatalf("xor set both: %#v", repair)
		}
	}
	if !containsString(repair.Remove, "zitadel.masterkeySecretName") {
		t.Fatalf("xor did not remove the other key: %#v", repair)
	}
}

func TestProposeRepairSetsNamedBooleans(t *testing.T) {
	err := errString("sumologic.sourcelessModeAck and sumologic.metrics.migrationDocAcknowledged are not set. set sumologic.sourcelessModeAck and sumologic.metrics.migrationDocAcknowledged to true")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.Sets, "sumologic.sourcelessModeAck=true") || !containsAssignment(repair.Sets, "sumologic.metrics.migrationDocAcknowledged=true") {
		t.Fatalf("bool repair = %#v", repair)
	}
}

func TestProposeRepairPrefixesSubchartBoolean(t *testing.T) {
	err := errString("Error: execution error at (artifactory-oss/charts/artifactory/templates/validations.yaml:60:8): set --set keysPassedViaSystemyaml=true")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.Sets, "artifactory.keysPassedViaSystemyaml=true") {
		t.Fatalf("subchart bool = %#v", repair)
	}
}

func TestProposeRepairPrefersTheSpecificFail(t *testing.T) {
	dir := t.TempDir()
	shared := "Chart needs attention before it can render on this cluster."
	writeRepairFile(t, dir, "templates/_helpers.tpl", `{{- define "first" -}}
{{- fail "`+shared+` Please set ready.enabled=true" -}}
{{- end -}}
{{- define "second" -}}
{{- if eq .Values.left.name .Values.right.name -}}
ok
{{- else -}}
{{- fail "`+shared+` The two names do not match." -}}
{{- end -}}
{{- end -}}
`)
	err := errString("Error: execution error at (demo/templates/app.yaml:4:8): " + shared + "\nThe two names do not match.")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "left.name="+repairPlaceholder) || !containsAssignment(repair.SetStrings, "right.name="+repairPlaceholder) {
		t.Fatalf("specific fail paths = %#v", repair)
	}
}

func TestProposeRepairUsesTheMatchingFail(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "templates/_helpers.tpl", `{{- define "first" -}}
{{- if not .Values.ready.enabled -}}
{{- fail "Chart needs attention. Please set, ready.enabled=true" -}}
{{- end -}}
{{- end -}}
{{- define "second" -}}
{{- if eq .Values.left.name .Values.right.name -}}
ok
{{- else -}}
{{- fail "Chart needs attention. The two names do not match." -}}
{{- end -}}
{{- end -}}
`)
	err := errString("Error: execution error at (demo/templates/app.yaml:4:8): Chart needs attention. The two names do not match.")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "left.name="+repairPlaceholder) || !containsAssignment(repair.SetStrings, "right.name="+repairPlaceholder) {
		t.Fatalf("matching fail paths = %#v", repair)
	}
}

func TestProposeRepairReadsExpectedStringFromTemplate(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "templates/secret.yaml", "password: {{ .Values.admin.password | b64enc }}\n")
	err := errString("Error: template: demo/templates/secret.yaml:1:40: executing \"demo/templates/secret.yaml\" at <b64enc>: invalid value; expected string")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "admin.password="+repairPlaceholder) {
		t.Fatalf("expected string = %#v", repair)
	}
}

func TestProposeRepairDropsRangedPlaceholder(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "charts/common/templates/_pvc.tpl", "{{- range $index, $PVC := .Values.persistence }}\n{{- if $PVC.enabled }}\n{{- end }}\n{{- end }}\n")
	err := errString(`Error: template: demo/charts/common/templates/_pvc.tpl:2:8: executing "common.pvc" at <$PVC.enabled>: can't evaluate field enabled in type interface {}`)
	repair := ProposeRepair(dir, err, RenderOptions{SetStrings: []string{"common.persistence.accessMode=" + repairPlaceholder, "common.persistence.size=" + repairPlaceholder}})
	if !containsString(repair.Remove, "common.persistence.accessMode") || !containsString(repair.Remove, "common.persistence.size") {
		t.Fatalf("ranged placeholders = %#v", repair)
	}
	rendered := withRepair(RenderOptions{SetStrings: []string{"common.persistence.accessMode=" + repairPlaceholder}}, repair)
	if containsAssignment(rendered.SetStrings, "common.persistence.accessMode="+repairPlaceholder) {
		t.Fatalf("removed placeholder stayed on the command: %#v", rendered.SetStrings)
	}
}

func TestProposeRepairFillsSchemaEnumInsteadOfDummyKey(t *testing.T) {
	dir := t.TempDir()
	writeRepairFile(t, dir, "Chart.yaml", "apiVersion: v2\nname: web\nversion: 0.1.0\n")
	writeRepairFile(t, dir, "values.schema.json", `{
  "type": "object",
  "properties": {
    "secretManagement": {
      "type": "object",
      "properties": {
        "database": {
          "type": "object",
          "properties": {
            "kek": {
              "type": "object",
              "properties": {
                "mode": {"enum": ["config", "keyset"]}
              }
            }
          }
        }
      }
    }
  }
}`)
	err := errString("Error: values don't meet the specifications of the schema(s) in the following chart(s):\nweb:\n- at '/secretManagement/database/kek': additional properties 'stageproofPlaceholder' not allowed\n")
	repair := ProposeRepair(dir, err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "secretManagement.database.kek.mode=config") {
		t.Fatalf("schema enum = %#v", repair)
	}
	if !containsString(repair.Remove, "secretManagement.database.kek.stageproofPlaceholder") {
		t.Fatalf("dummy key was not removed: %#v", repair)
	}
}

func TestProposeRepairSetsTheNamedAlternative(t *testing.T) {
	err := errString("Error: execution error at (demo/templates/app.yaml:4:8): secretManagement.database.kek is required. Either set secretManagement.database.kek.value (generate one), or reference secretManagement.database.kek.existingSecret.name.")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "secretManagement.database.kek.value="+repairPlaceholder) {
		t.Fatalf("named alternative = %#v", repair)
	}
}

func TestProposeRepairKeepsOneMutuallyExclusiveValue(t *testing.T) {
	err := errString("Error: execution error at (cost-analyzer/templates/NOTES.txt:9:4): global.federatedStorage.existingSecret and global.federatedStorage.config are mutually exclusive. Please specify only one.")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.SetStrings, "global.federatedStorage.existingSecret="+repairPlaceholder) {
		t.Fatalf("kept value = %#v", repair)
	}
	if !containsString(repair.Remove, "global.federatedStorage.config") {
		t.Fatalf("other value = %#v", repair)
	}
}

func TestProposeRepairTurnsStringIntoArray(t *testing.T) {
	err := errString("Error: values don't meet the specifications of the schema(s):\n- at '/agent/acquisition': got string, want array")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !containsAssignment(repair.SetJSON, `agent.acquisition=["`+repairPlaceholder+`"]`) {
		t.Fatalf("array repair = %#v", repair)
	}
	if !containsString(repair.Remove, "agent.acquisition") {
		t.Fatalf("array repair left the string key: %#v", repair)
	}
}

func TestProposeRepairDoesNotReplaceUserValue(t *testing.T) {
	dir := t.TempDir()
	values := writeRepairFile(t, dir, "user.yaml", "policy: sync\n")
	err := errString("Error: values don't meet the specifications of the schema(s):\n- at '/policy': got null, want string")
	repair := ProposeRepair(dir, err, RenderOptions{ValuesFiles: []string{values}})
	if len(repair.SetStrings) != 0 || len(repair.Sets) != 0 {
		t.Fatalf("user value was replaced: %#v", repair)
	}
}

func TestProposeRepairNamesAPIVersion(t *testing.T) {
	err := errString(`Error: execution error at (langfuse/templates/validations.yaml:107:8): ClickHouse operator CRDs not found (clickhouse.com/v1alpha1/ClickHouseCluster)`)
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if len(repair.APIVersions) != 1 || repair.APIVersions[0] != "clickhouse.com/v1alpha1/ClickHouseCluster" {
		t.Fatalf("api repair = %#v", repair)
	}
}

func TestProposeRepairSkipsInvalidSchemaDocument(t *testing.T) {
	err := errString(`Error: values don't meet the specifications of the schema(s) in the following chart(s):
code-server:
"file:///values.schema.json#" is not valid against metaschema: jsonschema validation failed`)
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if !repair.SkipSchema || repair.changesValues() {
		t.Fatalf("schema document repair = %#v", repair)
	}
}

func TestRepairDropsParentPlaceholder(t *testing.T) {
	repair := Repair{SetStrings: []string{"driver.config=" + repairPlaceholder, "driver.config.driver=" + repairPlaceholder}}
	repair.dropShadowedParents()
	if containsAssignment(repair.SetStrings, "driver.config="+repairPlaceholder) {
		t.Fatalf("parent placeholder stayed: %#v", repair.SetStrings)
	}
	if !containsAssignment(repair.SetStrings, "driver.config.driver="+repairPlaceholder) {
		t.Fatalf("child placeholder missing: %#v", repair.SetStrings)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func writeRepairFile(t *testing.T, dir, rel, body string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsAssignment(assignments []string, want string) bool {
	return containsString(assignments, want)
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestProposeRepairRejectsTraversalAndSymlinkEscape(t *testing.T) {
	outer := t.TempDir()
	secret := filepath.Join(outer, "outside.yaml")
	if err := os.WriteFile(secret, []byte("password: {{ .Values.leaked.secret | b64enc }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(outer, "chart")
	if err := os.MkdirAll(filepath.Join(chart, "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(chart, "templates", "escape.yaml")); err != nil {
		t.Skip(err)
	}

	traversalErr := errString("Error: template: chart/../outside.yaml:1:10: executing \"outside.yaml\" at <b64enc>: invalid value; expected string")
	if repair := ProposeRepair(chart, traversalErr, RenderOptions{}); !repair.Empty() {
		t.Fatalf("traversal read outside chartDir: %#v", repair)
	}

	symlinkErr := errString("Error: template: chart/templates/escape.yaml:1:10: executing \"escape.yaml\" at <b64enc>: invalid value; expected string")
	if repair := ProposeRepair(chart, symlinkErr, RenderOptions{}); !repair.Empty() {
		t.Fatalf("symlink read outside chartDir: %#v", repair)
	}
}

func TestProposeRepairRejectsUnsafeSetArguments(t *testing.T) {
	err := errString("Error: values don't meet the specifications of the schema(s) in the following chart(s):\nweb:\n- at '/a,b=c': got null, want string\n- at '/mode': value must be one of 'ok,evil=1'\n")
	repair := ProposeRepair(t.TempDir(), err, RenderOptions{})
	if repair.changesValues() {
		t.Fatalf("unsafe schema arguments were kept: %#v", repair)
	}
}

func TestValuesPathsIgnoreShorterParents(t *testing.T) {
	got := leafPaths([]string{"driver.config", "driver.config.driver"})
	if strings.Join(got, ",") != "driver.config.driver" {
		t.Fatalf("leaves = %v", got)
	}
}
