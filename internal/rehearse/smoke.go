package rehearse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/verdict"
)

// runSmoke runs each configured command after the upgrade and before rollback.
// A failure rolls the release back when there is a current chart, then stops.
func runSmoke(ctx context.Context, out *Outcome, kubeconfig, ns, relName, waitTimeout string, opts KindOptions) bool {
	for _, sm := range opts.Smoke {
		name := strings.TrimSpace(sm.Name)
		dur, dErr := sm.Duration()
		if dErr != nil {
			failSmoke(out, ns, relName, name, "Smoke test `"+name+"` has an invalid timeout.", dErr.Error(), opts.Secrets)
			rollbackAfterSmoke(ctx, out, kubeconfig, ns, relName, waitTimeout, opts)
			return false
		}
		sctx, cancel := context.WithTimeout(ctx, dur)
		text, err := runShell(sctx, kubeconfig, []string{
			"STAGEPROOF_NAMESPACE=" + ns,
			"STAGEPROOF_RELEASE=" + relName,
		}, sm.Run)
		timedOut := errors.Is(sctx.Err(), context.DeadlineExceeded)
		cancel()
		if err == nil && !timedOut {
			out.Checks = append(out.Checks, verdict.CheckResult{
				Name:    "smoke: " + name,
				Status:  "pass",
				Summary: "passed",
			})
			continue
		}
		msg := "Smoke test `" + name + "` failed."
		if timedOut {
			msg = "Smoke test `" + name + "` timed out."
		}
		failSmoke(out, ns, relName, name, msg, text, opts.Secrets)
		rollbackAfterSmoke(ctx, out, kubeconfig, ns, relName, waitTimeout, opts)
		return false
	}
	return true
}

func failSmoke(out *Outcome, ns, rel, name, msg, text string, secrets []string) {
	detail := helm.Scrub(lastLines(text, 20, 2048), secrets)
	msg = helm.Scrub(msg, secrets)
	out.Findings = append(out.Findings, verdict.Finding{
		RuleID:   "live.smoke_failed",
		Severity: verdict.SeverityBlocker,
		Resource: ns + "/" + rel,
		Message:  msg,
		Detail:   detail,
	})
	summary := detail
	if summary == "" {
		summary = msg
	}
	out.Checks = append(out.Checks, verdict.CheckResult{
		Name:    "smoke: " + name,
		Status:  "fail",
		Summary: truncateOneLine(summary, 120),
	})
	out.RuntimeSucceeded = false
}

func rollbackAfterSmoke(ctx context.Context, out *Outcome, kubeconfig, ns, relName, waitTimeout string, opts KindOptions) {
	if opts.BaseChartDir == "" {
		return
	}
	tRb := time.Now()
	rbErr := runHelmWaitWithFastFail(ctx, kubeconfig, ns, []string{"rollback", relName, "1", "--namespace", ns, "--wait", "--timeout", waitTimeout})
	if rbErr == nil {
		out.Checks = append(out.Checks, verdict.CheckResult{
			Name:    "rollback rehearsal",
			Status:  "pass",
			Summary: fmt.Sprintf("reverted cleanly to revision 1 in %s after a failed smoke test", time.Since(tRb).Round(100*time.Millisecond)),
		})
		return
	}
	detail := scrubRehearsal(rbErr.Error(), opts.Secrets)
	out.Findings = append(out.Findings, verdict.Finding{
		RuleID:   "helm.runtime.rollback_failed",
		Severity: verdict.SeverityBlocker,
		Resource: ns + "/" + relName,
		Message:  "Rollback to revision 1 ALSO failed after a failed smoke test.",
		Detail:   truncateOneLine(detail, 220),
	})
	out.Checks = append(out.Checks, verdict.CheckResult{
		Name:    "rollback rehearsal",
		Status:  "fail",
		Summary: truncateOneLine(detail, 120),
	})
}

// runShell runs a smoke command. The script is not copied into the error, so a
// value that appears only there is not printed. KUBECONFIG is set last.
func runShell(ctx context.Context, kubeconfig string, extra []string, script string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	configureShellCmd(cmd)
	env := os.Environ()
	env = append(env, extra...)
	if kubeconfig != "" {
		if abs, err := filepath.Abs(kubeconfig); err == nil {
			kubeconfig = abs
		}
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	text := stdout.String()
	if errText := stderr.String(); strings.TrimSpace(errText) != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += errText
	}
	return text, err
}
