package apiserver

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
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
	res, err := srv.Prove(ctx, []Manifest{beforeJob}, []Manifest{afterJob}, nil, ProofOptions{})
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
	hookRes, err := srv2.Prove(ctx, []Manifest{beforeJob}, []Manifest{hook}, nil, ProofOptions{})
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
	stsRes, err := srv3.Prove(ctx, []Manifest{beforeSTS}, []Manifest{afterSTS}, nil, ProofOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlocker(stsRes, "k8s.apply.immutable_field") {
		t.Fatalf("volumeClaimTemplates change must be immutable, findings: %+v", stsRes.Findings)
	}
	if len(stsRes.Stored) == 0 {
		t.Fatal("dry run must keep the stored object")
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
	res, err := srv.Prove(ctx, []Manifest{base}, []Manifest{changed}, nil, ProofOptions{})
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
	strict, err := srv.Prove(ctx, nil, []Manifest{changed}, nil, ProofOptions{StrictUnknown: true, CreatesOnly: true})
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
	if _, err := srv.Prove(ctx, nil, docs, nil, ProofOptions{CreatesOnly: true}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("50 objects took %s, the limit is 30s with a warm cache", elapsed)
	}
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
