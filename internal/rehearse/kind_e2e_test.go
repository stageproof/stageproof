package rehearse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndCLI_StaticAndLiveKindRehearsal(t *testing.T) {
	if os.Getenv("STAGEPROOF_KIND_E2E") == "" {
		t.Skip("set STAGEPROOF_KIND_E2E=1 to rehearse in a real kind cluster")
	}
	cliRoot := filepath.Clean(filepath.Join("..", ".."))
	binPath := filepath.Join(t.TempDir(), "stageproof")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/stageproof")
	buildCmd.Dir = cliRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build stageproof failed: %v\n%s", err, string(out))
	}

	work := t.TempDir()
	baseChart := filepath.Join(work, "chart-v1")
	goodChart := filepath.Join(work, "chart-v2-good")
	badKeyChart := filepath.Join(work, "chart-v2-dropped-key")
	crashChart := filepath.Join(work, "chart-v2-crash")
	tierCChart := filepath.Join(work, "chart-v2-tierc-crd")
	userVals := filepath.Join(work, "values-prod.yaml")

	// Baseline chart v1 (busybox HTTP/sleep pod)
	writeFixture(t, filepath.Join(baseChart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.0.0\n")
	writeFixture(t, filepath.Join(baseChart, "values.yaml"), "replicas: 1\nimage: busybox:1.36\nmessage: hello-v1\npodAnnotations: {}\n")
	writeFixture(t, filepath.Join(baseChart, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: demo-app
  template:
    metadata:
      labels:
        app: demo-app
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: app
          image: {{ .Values.image }}
          command: ["sh", "-c", "echo {{ .Values.message }} && sleep 3600"]
`)

	// Good upgrade chart v2: consumes all keys + adds label cleanly
	writeFixture(t, filepath.Join(goodChart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.1.0\n")
	writeFixture(t, filepath.Join(goodChart, "values.yaml"), "replicas: 1\nimage: busybox:1.36\nmessage: hello-v2\npodAnnotations: {}\n")
	writeFixture(t, filepath.Join(goodChart, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: demo-app
  template:
    metadata:
      labels:
        app: demo-app
        upgraded: "true"
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: app
          image: {{ .Values.image }}
          command: ["sh", "-c", "echo {{ .Values.message }} && sleep 3600"]
`)

	// Bad static chart v2: silently renames `.Values.message` -> `.Values.greeting`!
	writeFixture(t, filepath.Join(badKeyChart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 2.0.0\n")
	writeFixture(t, filepath.Join(badKeyChart, "values.yaml"), "replicas: 1\nimage: busybox:1.36\ngreeting: hello\npodAnnotations: {}\n")
	writeFixture(t, filepath.Join(badKeyChart, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: demo-app
  template:
    metadata:
      labels:
        app: demo-app
    spec:
      containers:
        - name: app
          image: {{ .Values.image }}
          command: ["sh", "-c", "echo {{ .Values.greeting }} && sleep 3600"]
`)

	// Bad runtime chart v2: passes helm lint & values AST, but crashes container at boot!
	writeFixture(t, filepath.Join(crashChart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.2.0\n")
	writeFixture(t, filepath.Join(crashChart, "values.yaml"), "replicas: 1\nimage: busybox:1.36\nmessage: hello\npodAnnotations: {}\n")
	writeFixture(t, filepath.Join(crashChart, "templates/deploy.yaml"), `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  replicas: {{ .Values.replicas }}
  selector:
    matchLabels:
      app: demo-app
  template:
    metadata:
      labels:
        app: demo-app
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: app
          image: {{ .Values.image }}
          command: ["sh", "-c", "echo {{ .Values.message }} && exit 1"]
          readinessProbe:
            exec:
              command: ["false"]
            initialDelaySeconds: 1
            periodSeconds: 1
`)

	// Tier C chart: references an unknown custom CRD (`acme.corp.io/v1alpha1 Widget`)
	writeFixture(t, filepath.Join(tierCChart, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: 1.3.0\n")
	writeFixture(t, filepath.Join(tierCChart, "values.yaml"), "replicas: 1\nimage: busybox:1.36\nmessage: hello\npodAnnotations: {}\n")
	writeFixture(t, filepath.Join(tierCChart, "templates/widget.yaml"), `
apiVersion: acme.corp.io/v1alpha1
kind: Widget
metadata:
  name: prod-widget
spec:
  message: {{ .Values.message }}
`)

	writeFixture(t, userVals, "replicas: 1\nmessage: custom-prod-banner\npodAnnotations:\n  env: prod\n")

	// Case 1: file checks only. PASSED exits 0. The report says what was checked and does not invent a warning.
	cmd1 := exec.Command(binPath, "check", "--current-chart", baseChart, "--new-chart", goodChart, "-f", userVals)
	out1, err1 := cmd1.CombinedOutput()
	if cmd1.ProcessState.ExitCode() != 0 {
		t.Fatalf("Case 1 expected exit 0 (PASSED), got %d (err=%v)\n%s", cmd1.ProcessState.ExitCode(), err1, string(out1))
	}
	if !strings.Contains(string(out1), "PASSED") || !strings.Contains(string(out1), "checked: files") {
		t.Fatalf("Case 1 must say PASSED and checked: files:\n%s", string(out1))
	}
	if strings.Contains(string(out1), "DEGRADED_RUNTIME_SCOPE") {
		t.Fatalf("Case 1 must not print a degraded banner:\n%s", string(out1))
	}

	// Case 2: Dropped values.yaml key -> MUST return exit 1 (BLOCKED)
	cmd2 := exec.Command(binPath, "check", "--current-chart", baseChart, "--new-chart", badKeyChart, "-f", userVals)
	out2, _ := cmd2.CombinedOutput()
	if cmd2.ProcessState.ExitCode() != 1 || !strings.Contains(string(out2), "helm.values.silently_dropped") {
		t.Fatalf("Case 2 expected exit 1 + helm.values.silently_dropped, got %d:\n%s", cmd2.ProcessState.ExitCode(), string(out2))
	}

	if os.Getenv("STAGEPROOF_SKIP_KIND") == "1" {
		t.Skip("STAGEPROOF_SKIP_KIND=1 set; skipping live kind cluster E2E")
	}

	clusterName := "stageproof-e2e-test"
	defer func() {
		_ = exec.Command("kind", "delete", "cluster", "--name", clusterName).Run()
	}()

	// Case 3: Live kind rehearsal (--live) on good upgrade -> MUST return exit 0 (PROVEN)
	cmd3 := exec.Command(binPath, "check", "--current-chart", baseChart, "--new-chart", goodChart, "-f", userVals,
		"--live", "--keep-cluster", "--cluster-name", clusterName, "--timeout", "45s")
	cmd3.Env = append(os.Environ(), "NO_COLOR=1")
	out3, err3 := cmd3.CombinedOutput()
	if cmd3.ProcessState.ExitCode() != 0 || !strings.Contains(string(out3), "PROVEN (exit 0)") {
		t.Fatalf("Case 3 expected exit 0 (PROVEN), got %d (err=%v):\n%s", cmd3.ProcessState.ExitCode(), err3, string(out3))
	}
	// The rehearsal must say which Kubernetes version it ran on. No --current-k8s
	// was given, so it is the default 1.33 and no mismatch warning may appear.
	if !strings.Contains(string(out3), "ready on Kubernetes v1.33.") || strings.Contains(string(out3), "rehearsal version") {
		t.Fatalf("Case 3 must rehearse on the requested Kubernetes version and report it:\n%s", string(out3))
	}

	// Case 4: Live kind rehearsal on crashing runtime upgrade -> MUST catch crashloop, rollback cleanly to rev 1, and exit 1 (BLOCKED)
	cmd4 := exec.Command(binPath, "check", "--current-chart", baseChart, "--new-chart", crashChart, "-f", userVals,
		"--live", "--keep-cluster", "--cluster-name", clusterName, "--timeout", "20s")
	out4, _ := cmd4.CombinedOutput()
	if cmd4.ProcessState.ExitCode() != 1 || !strings.Contains(string(out4), "rollback rehearsal") {
		t.Fatalf("Case 4 expected exit 1 (BLOCKED) + rollback rehearsal recovery, got %d:\n%s", cmd4.ProcessState.ExitCode(), string(out4))
	}

	// Case 5: a custom CRD that is not in the chart must not be reported as a broken upgrade. File checks passed, so the result is PASSED, and the report names the missing CRD.
	cmd5 := exec.Command(binPath, "check", "--current-chart", baseChart, "--new-chart", tierCChart, "-f", userVals,
		"--live", "--keep-cluster", "--cluster-name", clusterName, "--timeout", "30s")
	out5, _ := cmd5.CombinedOutput()
	if cmd5.ProcessState.ExitCode() != 0 || !strings.Contains(string(out5), "missing_external_crds") || !strings.Contains(string(out5), "PASSED") {
		t.Fatalf("Case 5 expected exit 0 (PASSED) + missing_external_crds, got %d:\n%s", cmd5.ProcessState.ExitCode(), string(out5))
	}
}
