package apiserver

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func apiServerOn(t *testing.T) string {
	t.Helper()
	if os.Getenv("STAGEPROOF_APISERVER") == "" {
		t.Skip("set STAGEPROOF_APISERVER=1 to start kube-apiserver")
	}
	v := os.Getenv("STAGEPROOF_APISERVER_K8S")
	if v == "" {
		v = "1.35"
	}
	return v
}

func TestAPIServerJobAndStatefulSet(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	beforeJob := manifest(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: migrate}
spec:
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: busybox:1.36}]
`)
	afterJob := manifest(t, `
apiVersion: batch/v1
kind: Job
metadata: {name: migrate}
spec:
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: busybox:1.37}]
`)
	hook := manifest(t, `
apiVersion: batch/v1
kind: Job
metadata:
  name: migrate
  annotations: {"helm.sh/hook": pre-upgrade}
spec:
  template:
    spec:
      restartPolicy: Never
      containers: [{name: job, image: busybox:1.37}]
`)
	res, err := srv.Prove(ctx, []Manifest{beforeJob}, []Manifest{afterJob}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(res, "k8s.apply.immutable_field") {
		t.Fatalf("job template change must be immutable, findings: %+v", res.Findings)
	}

	// A second server, because the first already stored the job.
	srv2, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv2.Stop() }()
	hookRes, err := srv2.Prove(ctx, []Manifest{beforeJob}, []Manifest{hook}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range hookRes.Findings {
		if f.Severity == "blocker" {
			t.Fatalf("a pre-upgrade hook must not block: %+v", hookRes.Findings)
		}
	}

	srv3, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv3.Stop() }()
	beforeSTS := manifest(t, sts("1Gi"))
	afterSTS := manifest(t, sts("2Gi"))
	stsRes, err := srv3.Prove(ctx, []Manifest{beforeSTS}, []Manifest{afterSTS}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(stsRes, "k8s.apply.immutable_field") {
		t.Fatalf("volumeClaimTemplates change must be immutable, findings: %+v", stsRes.Findings)
	}
	if len(stsRes.StoredBefore) == 0 {
		t.Fatal("the applied object must be kept")
	}
}

func TestAPIServerUnknownFieldShape(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	base := manifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: app}
spec:
  replicas: 1
  selector: {matchLabels: {app: app}}
  template:
    metadata: {labels: {app: app}}
    spec:
      containers: [{name: app, image: nginx:1.27}]
`)
	changed := manifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: app}
spec:
  replicas: 1
  selector: {matchLabels: {app: app}}
  template:
    metadata: {labels: {app: app}}
    spec:
      containers: [{name: app, image: nginx:1.27, notAField: x}]
`)
	res, err := srv.Prove(ctx, []Manifest{base}, []Manifest{changed}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(res, "k8s.apply.unknown_field") {
		t.Fatalf("strict decoding must be an unknown field, findings: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.RuleID == "k8s.apply.unknown_field" && f.Severity != "warning" {
			t.Fatalf("client-side apply drops the field, severity: %s", f.Severity)
		}
	}
	strict, err := srv.Prove(ctx, nil, []Manifest{changed}, nil, nil, ProofOptions{StrictUnknown: true, CreatesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range strict.Findings {
		if f.RuleID == "k8s.apply.unknown_field" && f.Severity != "blocker" {
			t.Fatalf("server-side apply rejects the field, severity: %s", f.Severity)
		}
	}
}

func TestAPIServerFiftyObjects(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Download and start once so the timed run uses the cache.
	warm, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = warm.Stop()

	var docs []Manifest
	for i := 0; i < 50; i++ {
		docs = append(docs, manifest(t, configMap(i)))
	}
	start := time.Now()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	if _, err := srv.Prove(ctx, nil, docs, nil, nil, ProofOptions{CreatesOnly: true}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("50 objects took %s, the limit is 30s with a warm cache", elapsed)
	}
}

func TestAPIServerCRDExistingObjectInvalid(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	if srv.Version() != version && !strings.HasPrefix(srv.Version(), version+".") {
		t.Fatalf("server reported %s for requested %s", srv.Version(), version)
	}

	beforeCRD := manifest(t, widgetCRD("string"))
	afterCRD := manifest(t, widgetCRD("integer"))
	widget := manifest(t, widgetCR())
	res, err := srv.Prove(ctx, []Manifest{beforeCRD, widget}, []Manifest{afterCRD, widget}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(res, "crd.existing_object_invalid") {
		t.Fatalf("stored widget must be invalid, findings: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.RuleID == "k8s.apply.not_exercised" && (f.Resource == "Widget/default/demo" || strings.Contains(f.Message, "Widget/default/demo")) {
			t.Fatalf("widget was not judged: %+v", res.Findings)
		}
	}

	srv2, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv2.Stop() }()
	control, err := srv2.Prove(ctx, []Manifest{beforeCRD, widget}, []Manifest{beforeCRD, widget}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(control, "crd.existing_object_invalid") {
		t.Fatalf("unchanged CRD must not block: %+v", control.Findings)
	}
}

func TestAPIServerSkipsWebhooks(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	dep := manifest(t, `
apiVersion: apps/v1
kind: Deployment
metadata: {name: app}
spec:
  replicas: 1
  selector: {matchLabels: {app: app}}
  template:
    metadata: {labels: {app: app}}
    spec:
      containers: [{name: app, image: nginx:1.27}]
`)
	hook := manifest(t, `
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingWebhookConfiguration
metadata: {name: deny}
webhooks:
  - name: deny.example.com
    admissionReviewVersions: ["v1"]
    sideEffects: None
    clientConfig: {url: "https://127.0.0.1:8443/deny"}
`)
	res, err := srv.Prove(ctx, []Manifest{dep}, []Manifest{dep, hook}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(res, "k8s.apply.not_exercised") {
		t.Fatalf("webhook must be skipped: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if f.Severity == "blocker" {
			t.Fatalf("deployment must still succeed: %+v", res.Findings)
		}
	}
}

func TestAPIServerInvalidEnumIsBlocker(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	pod := manifest(t, `
apiVersion: v1
kind: Pod
metadata: {name: bad}
spec:
  restartPolicy: Bogus
  containers: [{name: app, image: nginx:1.27}]
`)
	res, err := srv.Prove(ctx, nil, []Manifest{pod}, nil, nil, ProofOptions{CreatesOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(res, "k8s.apply.invalid") {
		t.Fatalf("invalid enum must block: %+v", res.Findings)
	}
	if hasRule(res, "k8s.apply.unknown_field") {
		t.Fatalf("invalid enum is not an unknown field: %+v", res.Findings)
	}
	for _, f := range res.Findings {
		if strings.Contains(f.Detail, "Bogus") || strings.Contains(f.Message, "Bogus") {
			t.Fatalf("rejected value leaked: %+v", f)
		}
	}
}

func widgetCRD(sizeType string) string {
	return `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: widgets.example.com}
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Widget, plural: widgets}
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                size: {type: ` + sizeType + `}
`
}

func widgetCR() string {
	return `
apiVersion: example.com/v1
kind: Widget
metadata: {name: demo}
spec: {size: large}
`
}

func TestAPIServerStopOnSIGTERM(t *testing.T) {
	version := apiServerOn(t)
	if os.Getenv("STAGEPROOF_SIGNAL_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		srv, err := Start(ctx, version, "")
		if err != nil {
			_, _ = os.Stderr.WriteString(err.Error())
			os.Exit(1)
		}
		_, _ = os.Stdout.WriteString(srv.Version() + "\n")
		<-ctx.Done()
		_ = srv.Stop()
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAPIServerStopOnSIGTERM$")
	cmd.Env = append(os.Environ(), "STAGEPROOF_SIGNAL_CHILD=1", "STAGEPROOF_APISERVER=1", "STAGEPROOF_APISERVER_K8S="+version)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) && !regexp.MustCompile(`\d+\.\d+\.\d+`).Match(output.Bytes()) {
		time.Sleep(200 * time.Millisecond)
	}
	if !regexp.MustCompile(`\d+\.\d+\.\d+`).Match(output.Bytes()) {
		_ = cmd.Process.Kill()
		t.Fatalf("server did not start:\n%s", output.String())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("child exited %v\n%s", err, output.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("SIGTERM did not stop the API server")
	}
	out, _ := exec.Command("ps", "-eo", "args").Output()
	if bytes.Contains(out, []byte("stageproof/envtest")) && bytes.Contains(out, []byte("kube-apiserver")) {
		t.Fatalf("kube-apiserver still running after SIGTERM:\n%s", out)
	}
}

func manifest(t *testing.T, doc string) Manifest {
	t.Helper()
	raw := map[string]any{}
	if err := yaml.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatal(err)
	}
	return Manifest{Raw: raw}
}

func hasRule(res ProofResult, id string) bool {
	for _, f := range res.Findings {
		if f.RuleID == id {
			return true
		}
	}
	return false
}

func hasBlocker(res ProofResult, id string) bool {
	for _, f := range res.Findings {
		if f.RuleID == id && f.Severity == "blocker" {
			return true
		}
	}
	return false
}

func sts(size string) string {
	return `
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: data}
spec:
  serviceName: data
  replicas: 1
  selector: {matchLabels: {app: data}}
  template:
    metadata: {labels: {app: data}}
    spec:
      containers: [{name: app, image: nginx:1.27}]
  volumeClaimTemplates:
    - metadata: {name: disk}
      spec:
        accessModes: ["ReadWriteOnce"]
        resources: {requests: {storage: ` + size + `}}
`
}

func configMap(i int) string {
	return `
apiVersion: v1
kind: ConfigMap
metadata: {name: cm-` + itoa(i) + `}
data: {n: "` + itoa(i) + `"}
`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func TestAPIServer_PreexistingInvalidDoesNotBlockUpgrade(t *testing.T) {
	version := apiServerOn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	srv, err := Start(ctx, version, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()

	crd := manifest(t, `
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata: {name: gadgets.example.com}
spec:
  group: example.com
  scope: Namespaced
  names: {kind: Gadget, plural: gadgets}
  versions:
    - name: v1
      served: true
      storage: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              required: [key]
              properties:
                key: {type: string}
                count: {type: integer}
`)
	badBefore := manifest(t, `
apiVersion: example.com/v1
kind: Gadget
metadata: {name: demo, namespace: default}
spec:
  key: null
`)
	fixedAfter := manifest(t, `
apiVersion: example.com/v1
kind: Gadget
metadata: {name: demo, namespace: default}
spec:
  key: ok
`)
	sameBadAfter := manifest(t, `
apiVersion: example.com/v1
kind: Gadget
metadata: {name: demo, namespace: default}
spec:
  key: null
`)
	worseAfter := manifest(t, `
apiVersion: example.com/v1
kind: Gadget
metadata: {name: demo, namespace: default}
spec:
  key: null
  count: "not-an-int"
`)

	// 1. Old chart had an invalid field that the new chart fixes -> 0 blockers.
	res, err := srv.Prove(ctx, []Manifest{crd, badBefore}, []Manifest{crd, fixedAfter}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(res, "k8s.apply.invalid") {
		t.Fatalf("fixing an old invalid object must not block: %+v", res.Findings)
	}

	// 2. Both charts fail on the exact same pre-existing invalid field -> warning, not blocker.
	res2, err := srv.Prove(ctx, []Manifest{crd, badBefore}, []Manifest{crd, sameBadAfter}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hasBlocker(res2, "k8s.apply.invalid") {
		t.Fatalf("pre-existing invalid field in both charts must not block: %+v", res2.Findings)
	}
	if !hasRule(res2, "k8s.apply.invalid") {
		t.Fatalf("expected warning for pre-existing invalid field, got %+v", res2.Findings)
	}

	// 3. New chart introduces a new invalid field -> must block.
	res3, err := srv.Prove(ctx, []Manifest{crd, badBefore}, []Manifest{crd, worseAfter}, nil, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(res3, "k8s.apply.invalid") {
		t.Fatalf("new invalid field introduced in upgrade must block: %+v", res3.Findings)
	}
}
