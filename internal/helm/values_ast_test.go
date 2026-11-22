package helm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/verdict"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiffValuesConsumption_CatchesDroppedKeyAndAvoidsWildcardFalsePositives(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, "chart-v1")
	headDir := filepath.Join(tmp, "chart-v2")
	userVals := filepath.Join(tmp, "values-prod.yaml")

	// Chart v1: uses alertmanager.config.route, replicas, and serializes extraEnv + podAnnotations: {}
	writeFile(t, filepath.Join(baseDir, "Chart.yaml"), "apiVersion: v2\nname: mon\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(baseDir, "values.yaml"), `
replicas: 2
podAnnotations: {}
alertmanager:
  config:
    route:
      receiver: default
`)
	writeFile(t, filepath.Join(baseDir, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mon
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  template:
    spec:
      containers:
        - name: app
          env:
            {{- range $k, $v := .Values.extraEnv }}
            - name: {{ $k }}
              value: {{ $v | quote }}
            {{- end }}
          args:
            - --receiver={{ .Values.alertmanager.config.route.receiver }}
`)

	// Chart v2: renames `alertmanager.config.route` -> `alertmanager.alertmanagerSpec.route`!
	writeFile(t, filepath.Join(headDir, "Chart.yaml"), "apiVersion: v2\nname: mon\nversion: 2.0.0\n")
	writeFile(t, filepath.Join(headDir, "values.yaml"), `
replicas: 2
podAnnotations: {}
alertmanager:
  alertmanagerSpec:
    route:
      receiver: default
`)
	writeFile(t, filepath.Join(headDir, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mon
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  template:
    spec:
      containers:
        - name: app
          env:
            {{- range $k, $v := .Values.extraEnv }}
            - name: {{ $k }}
              value: {{ $v | quote }}
            {{- end }}
          args:
            - --receiver={{ .Values.alertmanager.alertmanagerSpec.route.receiver }}
`)

	// User's prod values override: sets dynamic podAnnotations, dynamic extraEnv, global key, and old route
	writeFile(t, userVals, `
global:
  clusterName: prod-eu-1
replicas: 3
podAnnotations:
  prometheus.io/scrape: "true"
extraEnv:
  LOG_LEVEL: info
alertmanager:
  config:
    route:
      receiver: pagerduty-critical
`)

	findings, _, _, err := DiffValuesConsumption(baseDir, headDir, []string{userVals})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected exactly 1 blocker finding (zero false positives on podAnnotations/extraEnv/global), got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Severity != verdict.SeverityBlocker || f.RuleID != "helm.values.silently_dropped" {
		t.Fatalf("unexpected finding type: %+v", f)
	}
	if f.Resource != "alertmanager.config.route.receiver" {
		t.Fatalf("expected resource alertmanager.config.route.receiver, got %s", f.Resource)
	}
	if f.Detail == "" {
		t.Fatalf("expected rename suggestion in Detail, got empty")
	}
}

func TestK8sSchema_RemovedAPIAndImmutableSelector(t *testing.T) {
	baseYAML := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  selector:
    matchLabels:
      app: api
---
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: api-pdb
  namespace: prod
`
	headYAML := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  selector:
    matchLabels:
      app: api
      tier: backend
---
apiVersion: policy/v1beta1
kind: PodDisruptionBudget
metadata:
  name: api-pdb
  namespace: prod
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: api-sm
`
	baseObjs, err := k8sschema.ParseManifestStream(baseYAML)
	if err != nil {
		t.Fatal(err)
	}
	headObjs, err := k8sschema.ParseManifestStream(headYAML)
	if err != nil {
		t.Fatal(err)
	}

	compat := k8sschema.CheckCompatibility(headObjs, "1.33", "1.34", false)
	if len(compat) != 1 || compat[0].RuleID != "k8s.api.removed" {
		t.Fatalf("expected removed policy/v1beta1 blocker, got: %+v", compat)
	}

	// Verify >256KiB CRD warning includes exact 1-line fixes when SSA=false, and is suppressed when SSA=true
	bigCRD := k8sschema.ManifestObject{
		APIVersion: "apiextensions.k8s.io/v1",
		Kind:       "CustomResourceDefinition",
		Metadata:   k8sschema.ObjectMetadata{Name: "applicationsets.argoproj.io"},
		RawBytes:   300 * 1024,
	}
	warnNoSSA := k8sschema.CheckCompatibility([]k8sschema.ManifestObject{bigCRD}, "1.33", "", false)
	if len(warnNoSSA) != 1 || warnNoSSA[0].RuleID != "k8s.crd.oversized_annotation_limit" {
		t.Fatalf("expected oversized CRD warning when SSA=false, got: %+v", warnNoSSA)
	}
	warnWithSSA := k8sschema.CheckCompatibility([]k8sschema.ManifestObject{bigCRD}, "1.33", "", true)
	if len(warnWithSSA) != 0 {
		t.Fatalf("expected 0 warnings when --server-side-apply=true, got: %+v", warnWithSSA)
	}

	upg := k8sschema.DiffUpgradeSafety(baseObjs, headObjs)
	if len(upg) != 1 || upg[0].RuleID != "k8s.upgrade.immutable_selector_changed" {
		t.Fatalf("expected immutable selector blocker, got: %+v", upg)
	}

	extCRDs := k8sschema.DetectExternalCRDs(headObjs)
	if len(extCRDs) != 1 || extCRDs[0] != "monitoring.coreos.com/ServiceMonitor" {
		t.Fatalf("expected external CRD detection for ServiceMonitor, got: %+v", extCRDs)
	}
}

func TestValuesAST_DottedKeysWithOmitAndRequiredGate(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, "argo-v1")
	headSameReq := filepath.Join(tmp, "argo-v2-ok")
	headNewReq := filepath.Join(tmp, "argo-v2-new-req")
	vals := filepath.Join(tmp, "values.yaml")

	baseTmpl := `
{{- $redisPass := required "redis.password must be set" .Values.redis.password }}
{{- $cfg := omit .Values.configs.cm "create" -}}
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm
data:
  pass: {{ $redisPass | quote }}
`
	for _, d := range []string{baseDir, headSameReq} {
		writeFile(t, filepath.Join(d, "Chart.yaml"), "apiVersion: v2\nname: argo\nversion: 1.0.0\n")
		writeFile(t, filepath.Join(d, "values.yaml"), "configs:\n  cm:\n    create: true\nredis:\n  password: \"\"\n")
		writeFile(t, filepath.Join(d, "templates/cm.yaml"), baseTmpl)
	}

	writeFile(t, filepath.Join(headNewReq, "Chart.yaml"), "apiVersion: v2\nname: argo\nversion: 2.0.0\n")
	writeFile(t, filepath.Join(headNewReq, "values.yaml"), "configs:\n  cm:\n    create: true\nredis:\n  password: \"\"\nauth:\n  jwtSecret: \"\"\n")
	writeFile(t, filepath.Join(headNewReq, "templates/cm.yaml"), baseTmpl+"\n{{- $jwt := required \"auth.jwtSecret required in v2\" .Values.auth.jwtSecret }}\n")

	// Dotted YAML keys under `configs.cm` (Argo CD pattern)
	writeFile(t, vals, `
configs:
  cm:
    create: true
    timeout.reconciliation: "5s"
    application.resourceTrackingMethod: label
    "resource.customizations.ignoreDifferences.apiextensions.k8s.io_CustomResourceDefinition": "jsonPointers: []"
`)

	findings, _, _, err := DiffValuesConsumption(baseDir, headSameReq, []string{vals})
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 false warnings on dotted keys under `omit .Values.configs.cm`, got: %+v", findings)
	}

	// Existing `required` in both base & head -> synthesizes placeholder cleanly, 0 blockers
	ph, blockers := PlanRequiredPlaceholders(baseDir, headSameReq, []string{vals}, nil)
	if len(blockers) != 0 || len(ph) != 1 || ph[0] != "redis.password=stageproof-placeholder" {
		t.Fatalf("expected placeholder for shared required redis.password and 0 blockers, got ph=%v blockers=%+v", ph, blockers)
	}

	// Brand-new `required` added in v2 -> MUST block (`helm.values.new_required_unset`)!
	_, blockersNew := PlanRequiredPlaceholders(baseDir, headNewReq, []string{vals}, nil)
	if len(blockersNew) != 1 || blockersNew[0].RuleID != "helm.values.new_required_unset" {
		t.Fatalf("expected 1 new_required_unset blocker, got: %+v", blockersNew)
	}
}

func TestRequiredInsideIfIsNotADefaultBlocker(t *testing.T) {
	base := t.TempDir()
	head := t.TempDir()
	chart := "apiVersion: v2\nname: web\nversion: 0.1.0\n"
	writeFile(t, filepath.Join(base, "Chart.yaml"), chart)
	writeFile(t, filepath.Join(base, "values.yaml"), "tls:\n  enabled: false\n")
	writeFile(t, filepath.Join(base, "templates/deploy.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n")
	writeFile(t, filepath.Join(head, "Chart.yaml"), chart)
	writeFile(t, filepath.Join(head, "values.yaml"), "tls:\n  enabled: false\n  existingSecret: \"\"\n")
	writeFile(t, filepath.Join(head, "templates/deploy.yaml"), "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n{{- if .Values.tls.enabled }}\ndata:\n  secret: {{ required \"An existing secret is required to enable TLS\" .Values.tls.existingSecret }}\n{{- end }}\n")

	_, blockers := PlanRequiredPlaceholders(base, head, nil, nil)
	if len(blockers) != 0 {
		t.Fatalf("required inside if blocked a default upgrade: %+v", blockers)
	}
}
