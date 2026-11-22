package rehearse

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

func TestE2E_StaticAndLiveKindRehearsal(t *testing.T) {
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
  annotations:
    {{- toYaml .Values.podAnnotations | nindent 4 }}
spec:
  message: {{ .Values.message }}
`)

	writeFixture(t, userVals, "replicas: 1\nmessage: custom-prod-banner\npodAnnotations:\n  env: prod\n")

	// Case 1: file checks only. PASSED exits 0. The report says what was checked and does not invent a warning.
	cmd1 := exec.Command(binPath, "check", "--api-server", "off", "--current-chart", baseChart, "--new-chart", goodChart, "-f", userVals)
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
	cmd2 := exec.Command(binPath, "check", "--api-server", "off", "--current-chart", baseChart, "--new-chart", badKeyChart, "-f", userVals)
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

	// Case (a): a clean upgrade and rollback is PROVEN.
	// Case 3: Live kind rehearsal (--live) on good upgrade -> MUST return exit 0 (PROVEN)
	cmd3 := exec.Command(binPath, "check", "--api-server", "off", "--current-chart", baseChart, "--new-chart", goodChart, "-f", userVals,
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

	// Case (b): a new version that crash-loops is BLOCKED.
	// Case 4: Live kind rehearsal on crashing runtime upgrade -> MUST catch crashloop, rollback cleanly to rev 1, and exit 1 (BLOCKED)
	cmd4 := exec.Command(binPath, "check", "--api-server", "off", "--current-chart", baseChart, "--new-chart", crashChart, "-f", userVals,
		"--live", "--keep-cluster", "--cluster-name", clusterName, "--timeout", "20s")
	out4, _ := cmd4.CombinedOutput()
	if cmd4.ProcessState.ExitCode() != 1 || !strings.Contains(string(out4), "rollback rehearsal") {
		t.Fatalf("Case 4 expected exit 1 (BLOCKED) + rollback rehearsal recovery, got %d:\n%s", cmd4.ProcessState.ExitCode(), string(out4))
	}

	// Case 5: a custom CRD that is not in the chart must not be reported as a broken upgrade. File checks passed, so the result is PASSED, and the report names the missing CRD.
	cmd5 := exec.Command(binPath, "check", "--api-server", "off", "--current-chart", baseChart, "--new-chart", tierCChart, "-f", userVals,
		"--live", "--keep-cluster", "--cluster-name", clusterName, "--timeout", "30s")
	out5, _ := cmd5.CombinedOutput()
	if cmd5.ProcessState.ExitCode() != 0 || !strings.Contains(string(out5), "missing_external_crds") || !strings.Contains(string(out5), "PASSED") {
		t.Fatalf("Case 5 expected exit 0 (PASSED) + missing_external_crds, got %d:\n%s", cmd5.ProcessState.ExitCode(), string(out5))
	}
}

func e2eBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "stageproof")
	cmd := exec.Command("go", "build", "-o", binPath, "./cmd/stageproof")
	cmd.Dir = filepath.Clean(filepath.Join("..", ".."))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build stageproof failed: %v\n%s", err, string(out))
	}
	return binPath
}

func requireKindE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("STAGEPROOF_KIND_E2E") == "" {
		t.Skip("set STAGEPROOF_KIND_E2E=1 to rehearse in a real kind cluster")
	}
}

const tinyChart = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: demo-app
spec:
  replicas: 1
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
          command: ["sh", "-c", "sleep 3600"]
`

func writeTinyChart(t *testing.T, dir, version, image, extra string) {
	t.Helper()
	writeFixture(t, filepath.Join(dir, "Chart.yaml"), "apiVersion: v2\nname: demo\nversion: "+version+"\n")
	writeFixture(t, filepath.Join(dir, "values.yaml"), "image: \""+image+"\"\n")
	body := tinyChart
	if extra != "" {
		body = strings.Replace(body, "command: [\"sh\", \"-c\", \"sleep 3600\"]", "command: [\"sh\", \"-c\", \"sleep 3600\"]\n"+extra, 1)
	}
	writeFixture(t, filepath.Join(dir, "templates/deploy.yaml"), body)
}

// Case (c): the current release never becomes ready. The upgrade of that
// release still runs. PROVEN is allowed only when that upgraded release became ready.
func TestE2E_UnreadyBaselineThenUpgrade(t *testing.T) {
	requireKindE2E(t)
	bin := e2eBinary(t)
	work := t.TempDir()
	base := filepath.Join(work, "base")
	head := filepath.Join(work, "head")
	probe := `          readinessProbe:
            exec:
              command: ["false"]
            periodSeconds: 1
            failureThreshold: 1`
	writeTinyChart(t, base, "1.0.0", "busybox:1.36", probe)
	writeTinyChart(t, head, "1.1.0", "busybox:1.36", "")
	cluster := "sp-e2e-unready"
	defer func() { _ = exec.Command("kind", "delete", "cluster", "--name", cluster).Run() }()

	cmd := exec.Command(bin, "check", "--api-server", "off", "--current-chart", base, "--new-chart", head,
		"--live", "--cluster-name", cluster, "--timeout", "30s")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, _ := cmd.CombinedOutput()
	text := string(out)
	t.Log(text)
	for _, name := range []string{"baseline install", "in-place helm upgrade", "rollback rehearsal"} {
		if !strings.Contains(text, name) {
			t.Fatalf("missing check %q:\n%s", name, text)
		}
	}
	if strings.Contains(text, "candidate install") || strings.Contains(text, "not an upgrade of the current release") {
		t.Fatalf("the upgrade must use the release that already exists:\n%s", text)
	}
	if !strings.Contains(text, "baseline slow:") {
		t.Fatalf("the baseline deadline must be named:\n%s", text)
	}
	upgradedReady := strings.Contains(text, "upgraded the release that had not become ready")
	if strings.Contains(text, "PROVEN") && !upgradedReady {
		t.Fatalf("PROVEN requires the upgraded release to have become ready:\n%s", text)
	}
	if !strings.Contains(text, "NOT CHECKED") || strings.Contains(text, "PROVEN") || strings.Contains(text, "BLOCKED") {
		t.Fatalf("a rollback that hits the same deadline is NOT CHECKED:\n%s", text)
	}
	if !strings.Contains(text, "the current release still did not become ready") {
		t.Fatalf("rollback line missing:\n%s", text)
	}
}

// Case (d): an image tag that does not exist fails fast and names the image.
func TestE2E_MissingImageTag(t *testing.T) {
	requireKindE2E(t)
	bin := e2eBinary(t)
	work := t.TempDir()
	base := filepath.Join(work, "base")
	head := filepath.Join(work, "head")
	const image = "busybox:stageproof-no-such-tag"
	writeTinyChart(t, base, "1.0.0", image, "")
	writeTinyChart(t, head, "1.1.0", image, "")
	cluster := "sp-e2e-missing"
	defer func() { _ = exec.Command("kind", "delete", "cluster", "--name", cluster).Run() }()

	cmd := exec.Command(bin, "check", "--api-server", "off", "--current-chart", base, "--new-chart", head,
		"--live", "--cluster-name", cluster, "--timeout", "30s")
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, _ := cmd.CombinedOutput()
	text := string(out)
	if !strings.Contains(text, image) {
		t.Fatalf("the failure must name the image:\n%s", text)
	}
	if strings.Contains(text, "PROVEN") || strings.Contains(text, "baseline slow:") {
		t.Fatalf("a missing tag must fail before the readiness deadline:\n%s", text)
	}
	if !strings.Contains(text, "upstream image tag missing") {
		t.Fatalf("expected a missing-tag failure:\n%s", text)
	}
}

// Case (e): two rehearsals at once, with different cluster names, both finish.
func TestE2E_ParallelClusters(t *testing.T) {
	requireKindE2E(t)
	bin := e2eBinary(t)
	work := t.TempDir()
	base := filepath.Join(work, "base")
	head := filepath.Join(work, "head")
	writeTinyChart(t, base, "1.0.0", "busybox:1.36", "")
	writeTinyChart(t, head, "1.1.0", "busybox:1.36", "")
	names := []string{"sp-e2e-a", "sp-e2e-b"}
	defer func() {
		for _, name := range names {
			_ = exec.Command("kind", "delete", "cluster", "--name", name).Run()
		}
	}()

	var wg sync.WaitGroup
	texts := make([]string, len(names))
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			cmd := exec.Command(bin, "check", "--api-server", "off", "--current-chart", base, "--new-chart", head,
				"--live", "--cluster-name", name, "--timeout", "45s")
			cmd.Env = append(os.Environ(), "NO_COLOR=1")
			out, _ := cmd.CombinedOutput()
			texts[i] = string(out)
		}(i, name)
	}
	wg.Wait()
	for i, name := range names {
		if !strings.Contains(texts[i], "PROVEN") || !strings.Contains(texts[i], name) {
			t.Errorf("cluster %s did not finish on its own cluster:\n%s", name, texts[i])
		}
	}
}

func TestE2E_SmokePasses(t *testing.T) {
	requireKindE2E(t)
	text, code := runSmokeE2E(t, "sp-e2e-smoke-ok", `currentKubernetes: "1.33"
smoke:
  - name: deploy
    run: kubectl get deploy -n "$STAGEPROOF_NAMESPACE"
    timeout: 30s
`)
	t.Log(text)
	if code != 0 || !strings.Contains(text, "PROVEN") || !strings.Contains(text, "smoke: deploy") {
		t.Fatalf("exit %d\n%s", code, text)
	}
	if strings.Index(text, "smoke: deploy") > strings.Index(text, "rollback rehearsal") {
		t.Fatalf("smoke ran after rollback:\n%s", text)
	}
}

func TestE2E_SmokeFails(t *testing.T) {
	requireKindE2E(t)
	text, code := runSmokeE2E(t, "sp-e2e-smoke-fail", `currentKubernetes: "1.33"
smoke:
  - name: boom
    run: echo "smoke boom" && exit 1
    timeout: 30s
`)
	t.Log(text)
	if code != 1 || !strings.Contains(text, "BLOCKED") || !strings.Contains(text, "live.smoke_failed") || !strings.Contains(text, "smoke boom") {
		t.Fatalf("exit %d\n%s", code, text)
	}
	if strings.Index(text, "smoke: boom") > strings.Index(text, "rollback rehearsal") {
		t.Fatalf("smoke ran after rollback:\n%s", text)
	}
}

func runSmokeE2E(t *testing.T, cluster, cfg string) (string, int) {
	t.Helper()
	bin := e2eBinary(t)
	work := t.TempDir()
	base := filepath.Join(work, "base")
	head := filepath.Join(work, "head")
	writeTinyChart(t, base, "1.0.0", "busybox:1.36", "")
	writeTinyChart(t, head, "1.1.0", "busybox:1.36", "")
	writeFixture(t, filepath.Join(work, ".stageproof.yaml"), cfg)
	defer func() { _ = exec.Command("kind", "delete", "cluster", "--name", cluster).Run() }()
	cmd := exec.Command(bin, "check", "--api-server", "off", "--current-chart", base, "--new-chart", head,
		"--live", "--cluster-name", cluster, "--timeout", "90s")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	out, _ := cmd.CombinedOutput()
	code := 1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return string(out), code
}
