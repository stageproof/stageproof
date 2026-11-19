package rehearse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/verdict"
)

var KnownCRDBundles = map[string]string{
	"monitoring.coreos.com/ServiceMonitor": "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.78.1/example/prometheus-operator-crd/monitoring.coreos.com_servicemonitors.yaml",
	"monitoring.coreos.com/PodMonitor":     "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.78.1/example/prometheus-operator-crd/monitoring.coreos.com_podmonitors.yaml",
	"monitoring.coreos.com/PrometheusRule": "https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/v0.78.1/example/prometheus-operator-crd/monitoring.coreos.com_prometheusrules.yaml",
	"gateway.networking.k8s.io/HTTPRoute":  "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml",
	"gateway.networking.k8s.io/Gateway":    "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml",
	"cert-manager.io/Certificate":          "https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.crds.yaml",
	"cert-manager.io/Issuer":               "https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.crds.yaml",
	"cert-manager.io/ClusterIssuer":        "https://github.com/cert-manager/cert-manager/releases/download/v1.16.2/cert-manager.crds.yaml",
	"postgresql.cnpg.io/Cluster":           "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/v1.24.1/config/crd/bases/postgresql.cnpg.io_clusters.yaml",
}

type KindOptions struct {
	// KubernetesVersion is the version the user's cluster runs, for example "1.35".
	// The temporary cluster is created with the matching kind node image.
	KubernetesVersion string
	ClusterName       string
	KeepCluster       bool
	BaseChartDir      string
	HeadChartDir      string
	BaseValues        []string
	HeadValues        []string
	SetValues         []string
	SetStrings        []string
	ExternalCRDs      []string
	ExtraCRDFiles     []string
	Timeout           time.Duration
	RunHelmTest       bool
}

type Outcome struct {
	RuntimeSucceeded bool
	Inconclusive     bool
	ClusterName      string
	// Rehearsed is true once the candidate chart was installed or upgraded
	// in the temporary cluster. A skip before that point leaves it false.
	Rehearsed      bool
	Checks         []verdict.CheckResult
	Findings       []verdict.Finding
	Degraded       []verdict.DegradedNotice
	ExcludedChecks []string
}

// DefaultReadinessTimeout is how long one helm install, upgrade, or rollback
// waits for pods. Harbor and Longhorn do not become ready in 90s on an empty
// cluster, and a short wait then hides the candidate chart.
const DefaultReadinessTimeout = 10 * time.Minute

type failCategory int

const (
	failRegression      failCategory = iota // Deterministic app/chart breakage
	failExternalEnvGap                      // Missing private registry creds, external Secret/PVC, or unmocked external DB/service
	failUpstreamDeadTag                     // Public image tag deleted or missing. The rehearsal cannot finish.
)

func RunKindRehearsal(ctx context.Context, opts KindOptions) (*Outcome, error) {
	out := &Outcome{ClusterName: opts.ClusterName}
	if opts.ClusterName == "" {
		opts.ClusterName = "stageproof-ephemeral"
		out.ClusterName = opts.ClusterName
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultReadinessTimeout
	}

	kubeconfigFile, err := os.CreateTemp("", "stageproof-kubeconfig-*.yaml")
	if err != nil {
		return nil, err
	}
	kubeconfig := kubeconfigFile.Name()
	_ = kubeconfigFile.Close()
	defer os.Remove(kubeconfig)

	createdCluster, nodeImage, cErr := ensureKindCluster(ctx, opts.ClusterName, kubeconfig, opts.KubernetesVersion)
	if cErr != nil {
		out.Inconclusive = true
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "kind cluster boot",
			Status:  "fail",
			Summary: cErr.Error(),
		})
		return out, nil
	}
	if createdCluster && !opts.KeepCluster {
		defer func() {
			_, _ = runCmd(context.Background(), "", "kind", "delete", "cluster", "--name", opts.ClusterName)
		}()
	}
	// Say which Kubernetes version the rehearsal runs on. When it is not the
	// version the user asked for, say that too: a rehearsal on another version
	// proves less, and the reader must be able to see it.
	running := serverVersion(ctx, kubeconfig)
	bootSummary := fmt.Sprintf("isolated cluster `%s` ready", opts.ClusterName)
	if running != "" {
		bootSummary += " on Kubernetes " + running
	}
	out.Checks = append(out.Checks, verdict.CheckResult{Name: "kind cluster boot", Status: "pass", Summary: bootSummary})
	if want := minorOf(opts.KubernetesVersion); want != "" && running != "" && minorOf(running) != want {
		why := "no kind node image for " + want + " could be found"
		if !createdCluster {
			why = "an existing cluster named `" + opts.ClusterName + "` was reused"
		} else if nodeImage != "" {
			why = "kind started another version than " + nodeImage
		}
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "rehearsal version",
			Status:  "warn",
			Summary: fmt.Sprintf("ran on Kubernetes %s, not %s (%s)", running, want, why),
		})
	}

	unhydratedCRDs := hydrateCRDs(ctx, kubeconfig, opts.ExternalCRDs, opts.ExtraCRDFiles, out)
	if len(unhydratedCRDs) > 0 {
		out.Degraded = append(out.Degraded, verdict.DegradedNotice{
			Code:        "missing_external_crds",
			Reason:      fmt.Sprintf("Chart renders custom resources whose CRDs are not bundled or known: %s", strings.Join(unhydratedCRDs, ", ")),
			Remediation: "Pass `--crd-file <path-or-url>` (or set `crdFiles:` in `.stageproof.yaml`) and re-run with `--live`.",
		})
		out.ExcludedChecks = append(out.ExcludedChecks, "kind.upgrade_rollback")
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "CRD hydration",
			Status:  "warn",
			Summary: fmt.Sprintf("skipped: CRDs not available (%s)", strings.Join(unhydratedCRDs, ", ")),
		})
		return out, nil
	}

	ns := fmt.Sprintf("sp-trial-%d", time.Now().UnixNano()%1000000)
	if _, err := runCmd(ctx, kubeconfig, "kubectl", "create", "namespace", ns); err != nil {
		out.Inconclusive = true
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "namespace isolation",
			Status:  "fail",
			Summary: err.Error(),
		})
		return out, nil
	}
	defer func() {
		_, _ = runCmd(context.Background(), kubeconfig, "kubectl", "delete", "namespace", ns, "--wait=false", "--ignore-not-found=true")
	}()

	relName := "sp-rel"
	waitTimeout := fmt.Sprintf("%ds", int(opts.Timeout.Seconds()))
	// The baseline hit the deadline, but the release is still there.
	// The next step upgrades that release instead of installing the new chart alone.
	upgradedUnready := false

	// Step 1: Baseline installation (with fast-fail pod watcher so broken pulls/crashes abort in ~4s instead of 60s!)
	if opts.BaseChartDir != "" {
		t0 := time.Now()
		installArgs := []string{
			"install", relName, opts.BaseChartDir,
			"--namespace", ns,
			"--wait",
			"--timeout", waitTimeout,
		}
		for _, vf := range opts.BaseValues {
			installArgs = append(installArgs, "-f", vf)
		}
		for _, sv := range opts.SetValues {
			installArgs = append(installArgs, "--set", sv)
		}
		for _, ss := range opts.SetStrings {
			installArgs = append(installArgs, "--set-string", ss)
		}

		bErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, installArgs)
		if bErr != nil {
			diag, cat, rem := diagnoseNamespaceFailure(ctx, kubeconfig, ns, bErr.Error())
			if cat == failExternalEnvGap {
				out.Degraded = append(out.Degraded, verdict.DegradedNotice{
					Code:        "baseline_requires_external_dependency",
					Reason:      fmt.Sprintf("Baseline chart depends on an unmocked external resource (%s).", diag),
					Remediation: rem,
				})
				out.ExcludedChecks = append(out.ExcludedChecks, "kind.upgrade_rollback")
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "baseline install",
					Status:  "warn",
					Summary: diag,
				})
				return out, nil
			}

			if readinessTimedOut(bErr.Error()) {
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "baseline install",
					Status:  "warn",
					Summary: baselineSlowSummary(waitTimeout, diag),
				})
				// A release that exists can be upgraded with the same values.
				// Uninstalling it and installing the new chart alone runs hooks
				// an upgrade does not run.
				if helmReleasePresent(ctx, kubeconfig, ns, relName) {
					upgradedUnready = true
				} else {
					_, _ = runCmd(ctx, kubeconfig, "helm", "uninstall", relName, "--namespace", ns, "--wait", "--timeout", "2m", "--ignore-not-found")
					headArgs := helmInstallArgs(relName, opts.HeadChartDir, ns, waitTimeout, opts.HeadValues, opts.SetValues, opts.SetStrings)
					cErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, headArgs)
					if cErr != nil {
						cdiag, _, _ := diagnoseNamespaceFailure(ctx, kubeconfig, ns, cErr.Error())
						out.Inconclusive = true
						out.Checks = append(out.Checks, verdict.CheckResult{
							Name:    "candidate install",
							Status:  "fail",
							Summary: "The new chart's own install failed. This was not an upgrade of the current release. " + cdiag,
						})
						return out, nil
					}
					out.Degraded = append(out.Degraded, verdict.DegradedNotice{
						Code:        "baseline_not_ready",
						Reason:      "The current chart did not become ready, so this was not an upgrade rehearsal. The new chart was installed on its own and became ready.",
						Remediation: "Pass a larger --timeout, or a values file that lets the current chart become ready, to rehearse the upgrade itself.",
					})
					out.ExcludedChecks = append(out.ExcludedChecks, "kind.upgrade_rollback")
					out.Checks = append(out.Checks, verdict.CheckResult{
						Name:    "candidate install",
						Status:  "pass",
						Summary: "new chart became ready on its own. Existing data was not upgraded.",
					})
					return out, nil
				}
			}

			if !upgradedUnready {
				out.Inconclusive = true
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "baseline install",
					Status:  "fail",
					Summary: fmt.Sprintf("baseline itself failed before upgrade (%s)", diag),
				})
				return out, nil
			}
		} else {
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "baseline install",
				Status:  "pass",
				Summary: fmt.Sprintf("baseline revision 1 healthy in %s", time.Since(t0).Round(100*time.Millisecond)),
			})
		}
	}

	// From here the candidate chart is exercised in the temporary cluster.
	out.Rehearsed = true

	// Step 2a: If HeadChartDir ships `crds/*.yaml`, test Server-Side Apply CRD upgrade compatibility first
	headCRDsDir := filepath.Join(opts.HeadChartDir, "crds")
	if info, stErr := os.Stat(headCRDsDir); stErr == nil && info.IsDir() {
		if entries, _ := os.ReadDir(headCRDsDir); len(entries) > 0 {
			if _, crdErr := runCmd(ctx, kubeconfig, "kubectl", "apply", "--server-side", "--force-conflicts", "-f", headCRDsDir); crdErr != nil {
				out.Findings = append(out.Findings, verdict.Finding{
					RuleID:   "helm.crd.upgrade_failed",
					Severity: verdict.SeverityBlocker,
					Resource: headCRDsDir,
					Message:  "Upgraded chart CRDs failed Kubernetes Server-Side Apply.",
					Detail:   truncateOneLine(crdErr.Error(), 240),
				})
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "CRD schema upgrade",
					Status:  "fail",
					Summary: truncateOneLine(crdErr.Error(), 120),
				})
				return out, nil
			}
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "CRD schema upgrade",
				Status:  "pass",
				Summary: "server-side CRD upgrade clean",
			})
		}
	}

	// Step 2b: In-Place Upgrade (`helm upgrade --install --wait`) to HeadChartDir
	tUpg := time.Now()
	upgArgs := []string{
		"upgrade", "--install", relName, opts.HeadChartDir,
		"--namespace", ns,
		"--wait",
		"--timeout", waitTimeout,
	}
	for _, vf := range opts.HeadValues {
		upgArgs = append(upgArgs, "-f", vf)
	}
	for _, sv := range opts.SetValues {
		upgArgs = append(upgArgs, "--set", sv)
	}
	for _, ss := range opts.SetStrings {
		upgArgs = append(upgArgs, "--set-string", ss)
	}

	uErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, upgArgs)
	if uErr != nil {
		diag, cat, rem := diagnoseNamespaceFailure(ctx, kubeconfig, ns, uErr.Error())
		if opts.BaseChartDir == "" && cat == failExternalEnvGap {
			out.Rehearsed = false
			out.Degraded = append(out.Degraded, verdict.DegradedNotice{
				Code:        "head_requires_external_dependency",
				Reason:      fmt.Sprintf("Chart depends on an unmocked external resource (%s).", diag),
				Remediation: rem,
			})
			out.ExcludedChecks = append(out.ExcludedChecks, "kind.upgrade_rollback")
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "in-place helm upgrade",
				Status:  "warn",
				Summary: diag,
			})
			return out, nil
		}

		out.Findings = append(out.Findings, verdict.Finding{
			RuleID:   "helm.runtime.upgrade_failed",
			Severity: verdict.SeverityBlocker,
			Resource: ns + "/" + relName,
			Message:  fmt.Sprintf("In-place `helm upgrade` failed against healthy baseline: %s", diag),
			Detail:   truncateOneLine(uErr.Error(), 240),
		})
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "in-place helm upgrade",
			Status:  "fail",
			Summary: diag,
		})

		// Rehearse rollback to rev 1 even after failed upgrade!
		if opts.BaseChartDir != "" {
			tRb := time.Now()
			if rbErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, []string{"rollback", relName, "1", "--namespace", ns, "--wait", "--timeout", waitTimeout}); rbErr == nil {
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "rollback rehearsal",
					Status:  "pass",
					Summary: fmt.Sprintf("reverted cleanly to revision 1 in %s after failed upgrade", time.Since(tRb).Round(100*time.Millisecond)),
				})
			} else {
				out.Findings = append(out.Findings, verdict.Finding{
					RuleID:   "helm.runtime.rollback_failed",
					Severity: verdict.SeverityBlocker,
					Resource: ns + "/" + relName,
					Message:  "Rollback to revision 1 ALSO failed after broken upgrade.",
					Detail:   truncateOneLine(rbErr.Error(), 220),
				})
				out.Checks = append(out.Checks, verdict.CheckResult{
					Name:    "rollback rehearsal",
					Status:  "fail",
					Summary: truncateOneLine(rbErr.Error(), 120),
				})
			}
		}
		return out, nil
	}

	restarts, badPods := inspectPodHealth(ctx, kubeconfig, ns)
	if len(badPods) > 0 || restarts > 0 {
		msg := fmt.Sprintf("pods restarted (%d restarts) or unhealthy: %v", restarts, badPods)
		out.Findings = append(out.Findings, verdict.Finding{
			RuleID:   "helm.runtime.pod_restarts",
			Severity: verdict.SeverityBlocker,
			Resource: ns + "/" + relName,
			Message:  msg,
		})
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "in-place helm upgrade",
			Status:  "fail",
			Summary: msg,
		})
		return out, nil
	}

	out.Checks = append(out.Checks, verdict.CheckResult{
		Name:    "in-place helm upgrade",
		Status:  "pass",
		Summary: upgradeSummary(time.Since(tUpg), upgradedUnready),
	})

	if opts.RunHelmTest {
		if _, tErr := runCmd(ctx, kubeconfig, "helm", "test", relName, "--namespace", ns, "--timeout", waitTimeout); tErr != nil {
			out.Findings = append(out.Findings, verdict.Finding{
				RuleID:   "helm.runtime.helm_test_failed",
				Severity: verdict.SeverityBlocker,
				Resource: ns + "/" + relName,
				Message:  "Chart `helm test` suite failed after upgrade.",
				Detail:   truncateOneLine(tErr.Error(), 220),
			})
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "helm test",
				Status:  "fail",
				Summary: truncateOneLine(tErr.Error(), 120),
			})
			return out, nil
		}
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "helm test",
			Status:  "pass",
			Summary: "chart test hooks passed",
		})
	}

	if opts.BaseChartDir != "" {
		tRb := time.Now()
		if rbErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, []string{"rollback", relName, "1", "--namespace", ns, "--wait", "--timeout", waitTimeout}); rbErr != nil {
			out.Findings = append(out.Findings, verdict.Finding{
				RuleID:   "helm.runtime.rollback_failed",
				Severity: verdict.SeverityBlocker,
				Resource: ns + "/" + relName,
				Message:  "Upgrade succeeded, but `helm rollback` back to revision 1 failed!",
				Detail:   truncateOneLine(rbErr.Error(), 220),
			})
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "rollback rehearsal",
				Status:  "fail",
				Summary: truncateOneLine(rbErr.Error(), 120),
			})
			return out, nil
		}
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "rollback rehearsal",
			Status:  "pass",
			Summary: fmt.Sprintf("reverted to revision 1 cleanly in %s", time.Since(tRb).Round(100*time.Millisecond)),
		})
	}

	out.RuntimeSucceeded = true
	return out, nil
}

func helmInstallArgs(rel, chart, ns, timeout string, values, sets, setStrings []string) []string {
	args := []string{"install", rel, chart, "--namespace", ns, "--wait", "--timeout", timeout}
	for _, vf := range values {
		args = append(args, "-f", vf)
	}
	for _, sv := range sets {
		args = append(args, "--set", sv)
	}
	for _, ss := range setStrings {
		args = append(args, "--set-string", ss)
	}
	return args
}

// runHelmWaitWithFastFail executes `helm <args...>` while polling pods every 1.2s so
// fatal image-pull, missing-secret, or crashloop states abort immediately (~3-6s)
// instead of burning the entire 60-90s `--wait` timeout.
func runHelmWaitWithFastFail(parentCtx context.Context, kubeconfig, ns string, helmArgs []string) error {
	runCtx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	// Pods that were already failing before this step cannot be evidence that
	// this step failed. After a broken upgrade, the crashing pod of the bad
	// version is still there when the rollback starts.
	failingBefore := failingPods(parentCtx, kubeconfig, ns)

	var fastFailReason string
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(1200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if fatalReason := newFailure(failingPods(parentCtx, kubeconfig, ns), failingBefore); fatalReason != "" {
					fastFailReason = fatalReason
					cancel()
					return
				}
			}
		}
	}()

	_, err := runCmd(runCtx, kubeconfig, "helm", helmArgs...)
	cancel()
	<-done

	if fastFailReason != "" {
		return fmt.Errorf("aborted wait early on terminal pod state: %s", fastFailReason)
	}
	return err
}

// failingPods returns the pods in ns that are in a state they will not recover
// from on their own, keyed by pod name, with the reason.
func failingPods(ctx context.Context, kubeconfig, ns string) map[string]string {
	rawPods, err := runCmd(ctx, kubeconfig, "kubectl", "get", "pods", "-n", ns, "-o", "json")
	if err != nil {
		return nil
	}
	return failingPodsFromJSON([]byte(rawPods))
}

func failingPodsFromJSON(rawPods []byte) map[string]string {
	var podList struct {
		Items []struct {
			Metadata struct {
				Name              string  `json:"name"`
				DeletionTimestamp *string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses     []containerStateItem `json:"containerStatuses"`
				InitContainerStatuses []containerStateItem `json:"initContainerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(rawPods, &podList) != nil {
		return nil
	}
	failing := map[string]string{}
	for _, p := range podList.Items {
		// A pod that is being deleted is on its way out. Its state says nothing
		// about the release that replaces it.
		if p.Metadata.DeletionTimestamp != nil {
			continue
		}
		all := append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...)
		for _, cs := range all {
			reason := ""
			if cs.State.Waiting != nil {
				r := cs.State.Waiting.Reason
				msg := strings.ToLower(cs.State.Waiting.Message)
				switch r {
				case "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
					reason = fmt.Sprintf("%s (%s)", r, cs.State.Waiting.Message)
				case "ImagePullBackOff", "ErrImagePull":
					// A missing tag or a denied registry will not recover.
					// A timeout, a reset, or a rate limit often recovers if the
					// pull is left to retry, so those are not a fast failure.
					if hardImageFailure(msg) {
						reason = fmt.Sprintf("%s (%s)", r, cs.State.Waiting.Message)
					}
				case "CrashLoopBackOff":
					reason = fmt.Sprintf("CrashLoopBackOff (restarts=%d)", cs.RestartCount)
				}
			}
			if reason == "" && cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 && cs.RestartCount >= 1 {
				reason = fmt.Sprintf("exited %d (restarts=%d)", cs.State.Terminated.ExitCode, cs.RestartCount)
			}
			if reason != "" {
				failing[p.Metadata.Name] = reason
				break
			}
		}
	}
	return failing
}

// newFailure returns one pod that is failing now and was not failing before
// the step started, as "name: reason". It returns "" when there is none.
func baselineSlowSummary(waitTimeout, diag string) string {
	summary := fmt.Sprintf("baseline slow: the current chart did not become ready within %s. This is the empty-cluster install hitting the deadline, not a verdict that the chart is broken.", waitTimeout)
	if detail := baselineTimeoutDetail(diag); detail != "" {
		summary += " Detail: " + detail + "."
	}
	return summary
}

// baselineTimeoutDetail keeps the Helm deadline and drops the int64 warning
// that Helm prints on the same failure.
func baselineTimeoutDetail(diag string) string {
	low := strings.ToLower(diag)
	switch {
	case strings.Contains(low, "context deadline exceeded"):
		return "context deadline exceeded"
	case strings.Contains(low, "timed out"):
		return "timed out"
	default:
		return truncateOneLine(dropHelmNoise(diag), 240)
	}
}

func upgradeSummary(elapsed time.Duration, unready bool) string {
	took := elapsed.Round(100 * time.Millisecond)
	if unready {
		return fmt.Sprintf("upgraded the release that had not become ready in %s, 0 restarts. The same values were used.", took)
	}
	return fmt.Sprintf("empty cluster upgraded in %s, 0 restarts. No existing data was in this cluster.", took)
}

func helmReleasePresent(ctx context.Context, kubeconfig, ns, name string) bool {
	_, err := runCmd(ctx, kubeconfig, "helm", "status", name, "--namespace", ns)
	return err == nil
}

func dropHelmNoise(s string) string {
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.Contains(ln, `unrecognized format "int64"`) {
			continue
		}
		ln = strings.TrimSpace(ln)
		if ln != "" {
			kept = append(kept, ln)
		}
	}
	return strings.Join(kept, " ")
}

func readinessTimedOut(errText string) bool {
	l := strings.ToLower(errText)
	return strings.Contains(l, "timed out") || strings.Contains(l, "deadline exceeded")
}

func newFailure(now, before map[string]string) string {
	names := make([]string, 0, len(now))
	for name := range now {
		if _, old := before[name]; !old {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0] + ": " + now[names[0]]
}

type containerStateItem struct {
	Name         string `json:"name"`
	Ready        bool   `json:"ready"`
	RestartCount int    `json:"restartCount"`
	State        struct {
		Waiting *struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"waiting"`
		Terminated *struct {
			Reason   string `json:"reason"`
			ExitCode int    `json:"exitCode"`
		} `json:"terminated"`
	} `json:"state"`
}

const externalServiceHint = "The temporary cluster has no such service. Check with a values file (`-f`) that does not need it."

func diagnoseNamespaceFailure(ctx context.Context, kubeconfig, ns, helmErr string) (summary string, cat failCategory, remediation string) {
	// Every hint must name something the user can do today. The temporary cluster
	// is empty, so the way through is a values file that works in an empty cluster.
	defaultRem := "Check with a values file that works in an empty cluster (`-f`), for example a public image and no external database."

	rawPods, err := runCmd(ctx, kubeconfig, "kubectl", "get", "pods", "-n", ns, "-o", "json")
	if err == nil {
		var podList struct {
			Items []struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Status struct {
					Phase                 string               `json:"phase"`
					ContainerStatuses     []containerStateItem `json:"containerStatuses"`
					InitContainerStatuses []containerStateItem `json:"initContainerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(rawPods), &podList) == nil {
			for _, p := range podList.Items {
				all := append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...)
				for _, cs := range all {
					if cs.State.Waiting != nil {
						r := cs.State.Waiting.Reason
						msg := cs.State.Waiting.Message
						lmsg := strings.ToLower(msg)

						if r == "CreateContainerConfigError" {
							return fmt.Sprintf("pod `%s` missing external Secret/ConfigMap (%s)", p.Metadata.Name, truncateOneLine(msg, 90)),
								failExternalEnvGap,
								"Check with a values file (`-f`) that does not mount that Secret or ConfigMap, so the chart can start in an empty cluster."
						}
						if r == "ImagePullBackOff" || r == "ErrImagePull" {
							if strings.Contains(lmsg, "unauthorized") || strings.Contains(lmsg, "authentication") ||
								strings.Contains(lmsg, ".internal") || strings.Contains(lmsg, "harbor") ||
								strings.Contains(lmsg, "no such host") {
								return fmt.Sprintf("pod `%s` private registry image unreachable/unauthorized", p.Metadata.Name),
									failExternalEnvGap,
									"The temporary cluster cannot pull from a private registry. Check with a values file (`-f`) that uses an image it can pull."
							}
							if strings.Contains(lmsg, "not found") || strings.Contains(lmsg, "manifest unknown") {
								return fmt.Sprintf("pod `%s` upstream image tag missing (%s)", p.Metadata.Name, truncateOneLine(msg, 100)),
									failUpstreamDeadTag, defaultRem
							}
							return fmt.Sprintf("pod `%s` %s (%s)", p.Metadata.Name, r, truncateOneLine(msg, 90)), failRegression, defaultRem
						}
						if r == "CrashLoopBackOff" || r == "RunContainerError" {
							if extReason := checkContainerLogsForExternalService(ctx, kubeconfig, ns, p.Metadata.Name, cs.Name); extReason != "" {
								return fmt.Sprintf("pod `%s` waiting on unmocked external service (%s)", p.Metadata.Name, extReason),
									failExternalEnvGap,
									externalServiceHint
							}
							return fmt.Sprintf("pod `%s` %s (restarts=%d)", p.Metadata.Name, r, cs.RestartCount), failRegression, defaultRem
						}
					}
					if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
						logTail := tailContainerLog(ctx, kubeconfig, ns, p.Metadata.Name, cs.Name)
						if extReason := externalServiceLine(logTail); extReason != "" {
							return fmt.Sprintf("pod `%s` waiting on unmocked external service (%s)", p.Metadata.Name, extReason),
								failExternalEnvGap,
								externalServiceHint
						}
						return fmt.Sprintf("pod `%s` container `%s` exited %d (%s). log: %s", p.Metadata.Name, cs.Name, cs.State.Terminated.ExitCode, cs.State.Terminated.Reason, lastLines(logTail, 12, 800)),
							failRegression, defaultRem
					}
				}
			}
		}
	}

	eventsOut, _ := runCmd(ctx, kubeconfig, "kubectl", "get", "events", "-n", ns, "--field-selector=type=Warning")
	if strings.Contains(eventsOut, "unbound immediate PersistentVolumeClaims") || strings.Contains(eventsOut, "storageclass.storage.k8s.io") {
		return "custom StorageClass not present in ephemeral cluster",
			failExternalEnvGap,
			"Check with a values file (`-f`) that uses the storage class `standard`, the only one in the temporary cluster."
	}

	return truncateOneLine(helmErr, 800), failRegression, defaultRem
}

var externalConnIndicators = []string{
	"unknown mysql server host",
	"can't connect to mysql server",
	"could not translate host name",
	"name or service not known",
	"temporary failure in name resolution",
	"failed to connect to",
	"could not connect to",
	"is not reachable",
	"connection timed out",
	"dial tcp",
	"connection refused",
	"econnrefused",
	"enotfound",
	"database is uninitialized",
	"unable to connect to the database",
}

func tailContainerLog(ctx context.Context, kubeconfig, ns, podName, containerName string) string {
	logs, err := runCmd(ctx, kubeconfig, "kubectl", "logs", podName, "-n", ns, "-c", containerName, "--tail=40")
	if err != nil || strings.TrimSpace(logs) == "" {
		logs, _ = runCmd(ctx, kubeconfig, "kubectl", "logs", podName, "-n", ns, "-c", containerName, "--previous", "--tail=40")
	}
	return strings.TrimSpace(logs)
}

func checkContainerLogsForExternalService(ctx context.Context, kubeconfig, ns, podName, containerName string) string {
	return externalServiceLine(tailContainerLog(ctx, kubeconfig, ns, podName, containerName))
}

func externalServiceLine(logs string) string {
	lower := strings.ToLower(logs)
	for _, indicator := range externalConnIndicators {
		if !strings.Contains(lower, indicator) {
			continue
		}
		for _, line := range strings.Split(logs, "\n") {
			if !strings.Contains(strings.ToLower(line), indicator) {
				continue
			}
			// The cluster's own API is not an outside service. An admission
			// job that calls it is part of installing the chart.
			if inClusterAPI(line) {
				continue
			}
			return truncateOneLine(line, 95)
		}
	}
	return ""
}

func inClusterAPI(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "kubernetes.default") ||
		strings.Contains(l, "kube-apiserver") ||
		strings.Contains(l, "serviceaccount") ||
		strings.Contains(l, "apiserver") ||
		strings.Contains(l, "/api/")
}

// ensureKindCluster returns a running cluster. A new cluster is created on the
// Kubernetes version the user's cluster runs, when kind publishes a node image
// for it. nodeImage is that image, or empty when kind's default was used.
func ensureKindCluster(ctx context.Context, name, kubeconfig, k8sVersion string) (created bool, nodeImage string, err error) {
	clustersOut, err := runCmd(ctx, "", "kind", "get", "clusters")
	if err != nil {
		return false, "", fmt.Errorf("kind get clusters failed: %w", err)
	}
	exists := false
	for _, line := range strings.Split(clustersOut, "\n") {
		if strings.TrimSpace(line) == name {
			exists = true
			break
		}
	}
	if !exists {
		args := []string{"create", "cluster", "--name", name, "--wait", "60s"}
		nodeImage = resolveNodeImage(ctx, k8sVersion)
		if nodeImage != "" {
			args = append(args, "--image", nodeImage)
		}
		if _, err := runCmd(ctx, kubeconfig, "kind", args...); err != nil {
			if nodeImage != "" {
				return false, nodeImage, fmt.Errorf("kind could not start Kubernetes %s with %s. A newer kind release may be needed for this version: %w", minorOf(k8sVersion), nodeImage, err)
			}
			return false, "", fmt.Errorf("kind create cluster %s: %w", name, err)
		}
		return true, nodeImage, nil
	}
	if _, err := runCmd(ctx, "", "kind", "export", "kubeconfig", "--name", name, "--kubeconfig", kubeconfig); err != nil {
		return false, "", fmt.Errorf("kind export kubeconfig: %w", err)
	}
	return false, "", nil
}

// nodeImageTagsURL lists the published kind node images. Tests point it elsewhere.
var nodeImageTagsURL = "https://hub.docker.com/v2/repositories/kindest/node/tags"

// resolveNodeImage finds the kind node image for a Kubernetes version such as
// "1.35". kind publishes images for specific patch releases only, so the newest
// patch of that minor version is looked up. It returns "" when the lookup fails
// or nothing is published for that version, and kind then uses its default.
func resolveNodeImage(ctx context.Context, k8sVersion string) string {
	for attempt := 0; attempt < 3; attempt++ {
		img, retry := resolveNodeImageOnce(ctx, k8sVersion)
		if img != "" || !retry {
			return img
		}
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
	return ""
}

func resolveNodeImageOnce(ctx context.Context, k8sVersion string) (string, bool) {
	minor := minorOf(k8sVersion)
	if minor == "" {
		return "", false
	}
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, nodeImageTagsURL+"?page_size=100&name=v"+minor+".", nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", true
	}
	var page struct {
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}
	if json.NewDecoder(resp.Body).Decode(&page) != nil {
		return "", true
	}
	names := make([]string, 0, len(page.Results))
	for _, r := range page.Results {
		names = append(names, r.Name)
	}
	if tag := latestPatchTag(names, minor); tag != "" {
		return "kindest/node:" + tag, false
	}
	return "", false
}

func hardImageFailure(msg string) bool {
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "manifest unknown") ||
		strings.Contains(msg, "no such manifest") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "authentication") ||
		strings.Contains(msg, "no such host")
}

func lastLines(s string, n, max int) string {
	var kept []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			kept = append(kept, ln)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	out := strings.Join(kept, " | ")
	if len(out) > max && max > 0 {
		return out[len(out)-max:]
	}
	return out
}

// latestPatchTag picks the highest patch release of minor ("1.35") from image
// tags such as "v1.35.0" and "v1.35.8". Tags of other versions are ignored.
func latestPatchTag(tags []string, minor string) string {
	best, bestPatch := "", -1
	prefix := "v" + minor + "."
	for _, tag := range tags {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		patch, err := strconv.Atoi(strings.TrimPrefix(tag, prefix))
		if err != nil || patch < 0 {
			continue
		}
		if patch > bestPatch {
			best, bestPatch = tag, patch
		}
	}
	return best
}

// minorOf reduces "v1.35.8", "1.35.8" or "1.35" to "1.35". It returns "" for
// anything that is not a version.
func minorOf(version string) string {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts[:2] {
		if _, err := strconv.Atoi(p); err != nil {
			return ""
		}
	}
	return parts[0] + "." + parts[1]
}

// serverVersion returns the Kubernetes version of the temporary cluster, for
// example "v1.35.8", or "" when it cannot be read.
func serverVersion(ctx context.Context, kubeconfig string) string {
	raw, err := runCmd(ctx, kubeconfig, "kubectl", "version", "-o", "json")
	if err != nil {
		return ""
	}
	var v struct {
		ServerVersion struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return ""
	}
	return v.ServerVersion.GitVersion
}

func countedKinds(n int) string {
	if n == 1 {
		return "1 external CRD type"
	}
	return fmt.Sprintf("%d external CRD types", n)
}

func hydrateCRDs(ctx context.Context, kubeconfig string, externalGKs, extraBundles []string, out *Outcome) []string {
	appliedURLs := make(map[string]bool)
	for _, bundle := range extraBundles {
		_, _ = runCmd(ctx, kubeconfig, "kubectl", "apply", "--server-side", "-f", bundle)
		appliedURLs[bundle] = true
	}

	var missing []string
	hydratedCount := 0
	for _, gk := range externalGKs {
		url, ok := KnownCRDBundles[gk]
		if !ok {
			missing = append(missing, gk)
			continue
		}
		if !appliedURLs[url] {
			if _, err := runCmd(ctx, kubeconfig, "kubectl", "apply", "--server-side", "-f", url); err != nil {
				missing = append(missing, gk)
				continue
			}
			appliedURLs[url] = true
		}
		hydratedCount++
	}
	if hydratedCount > 0 && len(missing) == 0 {
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "CRD hydration",
			Status:  "pass",
			Summary: fmt.Sprintf("installed %s", countedKinds(hydratedCount)),
		})
	}
	return missing
}

func inspectPodHealth(ctx context.Context, kubeconfig, ns string) (totalRestarts int, unhealthy []string) {
	rawPods, err := runCmd(ctx, kubeconfig, "kubectl", "get", "pods", "-n", ns, "-o", "json")
	if err != nil {
		return 0, nil
	}
	var podList struct {
		Items []struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
			Status struct {
				Phase             string               `json:"phase"`
				ContainerStatuses []containerStateItem `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal([]byte(rawPods), &podList) != nil {
		return 0, nil
	}
	for _, p := range podList.Items {
		if _, isHook := p.Metadata.Annotations["helm.sh/hook"]; isHook {
			continue
		}
		if p.Status.Phase == "Succeeded" {
			continue
		}
		for _, cs := range p.Status.ContainerStatuses {
			totalRestarts += cs.RestartCount
			if !cs.Ready {
				unhealthy = append(unhealthy, p.Metadata.Name)
			}
		}
	}
	return totalRestarts, unhealthy
}

func runCmd(ctx context.Context, kubeconfig, bin string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	env := os.Environ()
	if kubeconfig != "" {
		absKube, err := filepath.Abs(kubeconfig)
		if err == nil {
			kubeconfig = absKube
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("%s %s: %w (%s)", bin, strings.Join(args, " "), err, errMsg)
	}
	return stdout.String(), nil
}

func truncateOneLine(s string, maxLen int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}
