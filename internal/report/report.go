package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stageproof/stageproof/internal/verdict"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31;1m"
	ansiYellow = "\033[33;1m"
	ansiGreen  = "\033[32m"
	ansiGreenB = "\033[32;1m"
	ansiCyan   = "\033[36m"
	ansiDim    = "\033[2m"
)

// ColorEnabled is true when the job log or a terminal can show ANSI colors.
// GitLab and GitHub capture stdout through a pipe, so a TTY check alone stays
// plain in CI. NO_COLOR and STAGEPROOF_COLOR=never win.
func ColorEnabled(w io.Writer) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("STAGEPROOF_COLOR"))) {
	case "always", "1", "true", "yes":
		return true
	case "never", "0", "false", "no":
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("GITLAB_CI") == "true" || os.Getenv("CI") == "true" {
		return true
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

func paint(enabled bool, code, text string) string {
	if !enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

func WriteJSON(w io.Writer, r *verdict.Report) error {
	if r != nil {
		r.SchemaVersion = 1
		r.Findings = OrderFindings(r.Findings)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func WriteTerminal(w io.Writer, r *verdict.Report) {
	writeTerminal(w, r, ColorEnabled(w))
}

func writeTerminal(w io.Writer, r *verdict.Report, color bool) {
	if r != nil {
		r.Findings = OrderFindings(r.Findings)
	}
	verdictColor := ansiGreenB
	switch r.Verdict {
	case verdict.Blocked:
		verdictColor = ansiRed
	case verdict.NotChecked:
		verdictColor = ansiYellow
	case verdict.Passed:
		verdictColor = ansiGreen
	}
	for _, line := range verdictHeadlines(r) {
		mark := ansiYellow
		if strings.HasPrefix(line, "NOT TESTED LIVE") {
			mark = ansiRed
		}
		fmt.Fprintf(w, "%s  %s\n", paint(color, mark, "!"), paint(color, mark, line))
	}
	fmt.Fprintf(w, "%s  %s  %s\n",
		paint(color, ansiDim, "stageproof"),
		paint(color, verdictColor, string(r.Verdict)),
		paint(color, ansiDim, fmt.Sprintf("exit %d", r.ExitCode)),
	)
	fmt.Fprintf(w, "%s  %s %s %s\n",
		paint(color, ansiDim, "compare"),
		r.BaseRef,
		paint(color, ansiDim, "->"),
		r.HeadRef,
	)
	if len(r.ValuesFiles) > 0 {
		fmt.Fprintf(w, "%s  %s\n", paint(color, ansiDim, "values"), strings.Join(r.ValuesFiles, ", "))
	}
	k8sLine := r.TargetK8s
	if r.NextK8sEOL != "" {
		k8sLine += "  next " + r.NextK8sEOL
	}
	checked := r.Checked
	if checked == "" {
		checked = verdict.CheckedFiles
	}
	fmt.Fprintf(w, "%s  %s  %s  %s\n\n",
		paint(color, ansiDim, "k8s"),
		k8sLine,
		paint(color, ansiDim, "·"),
		paint(color, ansiDim, "checked: "+checked),
	)
	writeBrief(w, r, color)

	for _, ck := range r.Checks {
		sym, code, label := "•", ansiDim, "    "
		switch ck.Status {
		case "pass":
			sym, code, label = "✓", ansiGreen, "PASS"
		case "fail":
			sym, code, label = "✕", ansiRed, "FAIL"
		case "warn":
			sym, code, label = "!", ansiYellow, "WARN"
		case "skip":
			sym, code, label = "○", ansiDim, "SKIP"
		}
		fmt.Fprintf(w, "  %s %s  %-24s %s\n",
			paint(color, code, sym),
			paint(color, code, label),
			ck.Name,
			ck.Summary,
		)
	}

	if len(r.Findings) > 0 {
		fmt.Fprintln(w)
		for _, f := range r.Findings {
			code, label := ansiCyan, "INFO"
			switch f.Severity {
			case verdict.SeverityBlocker:
				code, label = ansiRed, "ERROR"
			case verdict.SeverityWarning:
				code, label = ansiYellow, "WARN"
			}
			loc := f.Resource
			if f.File != "" {
				if loc != "" {
					loc = f.File + " (" + loc + ")"
				} else {
					loc = f.File
				}
			}
			fmt.Fprintf(w, "  %s  %s\n", paint(color, code, label), paint(color, code, f.RuleID))
			if loc != "" {
				fmt.Fprintf(w, "         %s\n", paint(color, ansiDim, loc))
			}
			fmt.Fprintf(w, "         %s\n", f.Message)
			if f.Ignored {
				fmt.Fprintf(w, "         ignored until %s: %s\n", f.IgnoreExpires, oneLine(f.IgnoreReason))
			}
			if f.Detail != "" {
				fmt.Fprintf(w, "         %s\n", paint(color, ansiDim, f.Detail))
			}
		}
	}

	if len(r.Skipped) > 0 {
		fmt.Fprintln(w)
		for _, d := range r.Skipped {
			fmt.Fprintf(w, "  %s %s  %s\n", paint(color, ansiYellow, "!"), paint(color, ansiYellow, "WARN"), d.Code)
			fmt.Fprintf(w, "         %s\n", d.Reason)
			if d.Remediation != "" {
				fmt.Fprintf(w, "         %s %s\n", paint(color, ansiYellow, "fix:"), d.Remediation)
			}
		}
	}

	fmt.Fprintf(w, "\n%s  %s  %s\n",
		paint(color, ansiDim, "verdict"),
		paint(color, verdictColor, fmt.Sprintf("%s (exit %d)", r.Verdict, r.ExitCode)),
		paint(color, ansiDim, fmt.Sprintf("· %dms · 0 AI calls", r.DurationMs)),
	)
}

// Combined is the result of a pull request check: the worst result across all
// charts that changed, and the report of each one.
type Combined struct {
	SchemaVersion int               `json:"schema_version"`
	Verdict       verdict.State     `json:"verdict"`
	ExitCode      int               `json:"exit_code"`
	HeadGit       string            `json:"head_git,omitempty"`
	Reports       []*verdict.Report `json:"reports"`
}

func WriteCombinedJSON(w io.Writer, c *Combined) error {
	if c != nil {
		c.SchemaVersion = 1
		for _, r := range c.Reports {
			if r != nil {
				r.SchemaVersion = 1
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(c)
}

// WriteCombinedMarkdown writes the pull request comment. One chart gets its
// report as is. Several charts get a summary table first, then every report.
func WriteCombinedMarkdown(w io.Writer, c *Combined) {
	if len(c.Reports) == 1 {
		WriteMarkdown(w, c.Reports[0])
		return
	}
	fmt.Fprintf(w, "## %s Stageproof: `%s` (exit `%d`) across %d checks\n\n", verdictIcon(c.Verdict), c.Verdict, c.ExitCode, len(c.Reports))
	fmt.Fprintln(w, "| Compared | Values | Result |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, r := range c.Reports {
		values := "chart defaults"
		if len(r.ValuesFiles) > 0 {
			values = "`" + strings.Join(r.ValuesFiles, "`, `") + "`"
		}
		fmt.Fprintf(w, "| `%s` → `%s` | %s | %s **%s** |\n", mdCell(r.BaseRef), mdCell(r.HeadRef), mdCell(values), verdictIcon(r.Verdict), r.Verdict)
	}
	for _, r := range c.Reports {
		fmt.Fprint(w, "\n---\n\n")
		writeMarkdownBody(w, r)
	}
	writeMarkdownFooter(w)
}

func verdictHeadlines(r *verdict.Report) []string {
	var lines []string
	for _, f := range r.Findings {
		if f.Severity != verdict.SeverityBlocker || f.Ignored {
			continue
		}
		if strings.HasPrefix(f.RuleID, "impact.delete") || strings.HasPrefix(f.RuleID, "crd.") {
			lines = append(lines, f.Message)
		}
	}
	if strings.Contains(r.Checked, "live cluster") && minorMismatch(r) {
		lines = append(lines, "Rehearsed on a different Kubernetes version than the one requested. This proves less.")
	}
	for _, f := range r.Findings {
		if f.Ignored {
			continue
		}
		if f.RuleID == "helm.dependency.not_updated" {
			lines = append(lines, "The packaged chart under charts/ did not change. This is not proof of the new dependency version.")
			break
		}
	}
	if r.Cluster != "" {
		lines = append([]string{"kind cluster " + r.Cluster}, lines...)
	}
	if r.Verdict == verdict.Passed && !strings.Contains(r.Checked, "live cluster") {
		lines = append(lines, "NOT TESTED LIVE. This job did not rehearse the upgrade. The files were checked. The upgrade itself was not.")
	}
	if r.Verdict == verdict.Proven {
		lines = append(lines, "This rehearsal used an empty cluster. No existing data was in it.")
		if baselineWasSlow(r) {
			lines = append(lines, "The current release had not become ready. The upgrade used that release and the same values.")
		}
		if msg := operatorNote(r); msg != "" {
			lines = append(lines, msg)
		}
	}
	return lines
}

func operatorNote(r *verdict.Report) string {
	for _, id := range []string{"helm.operator.documented_upgrade_behavior", "helm.operator.primary_restart"} {
		for _, f := range r.Findings {
			if f.RuleID == id && f.Message != "" {
				return f.Message
			}
		}
	}
	return ""
}

func baselineWasSlow(r *verdict.Report) bool {
	for _, ck := range r.Checks {
		if ck.Name == "baseline install" && ck.Status == "warn" && strings.Contains(ck.Summary, "baseline slow") {
			return true
		}
	}
	return false
}

func minorMismatch(r *verdict.Report) bool {
	for _, ck := range r.Checks {
		if ck.Name == "rehearsal version" && ck.Status == "warn" {
			return true
		}
	}
	return false
}

func verdictIcon(v verdict.State) string {
	switch v {
	case verdict.Proven, verdict.Passed:
		return "✅"
	case verdict.Blocked:
		return "❌"
	default:
		return "⚠️"
	}
}

func checkStatusMarkdown(status string) string {
	switch strings.ToLower(status) {
	case "pass":
		return "✅ PASS"
	case "warn":
		return "⚠️ WARN"
	case "fail":
		return "❌ FAIL"
	case "skip":
		return "⏭️ SKIP"
	default:
		return strings.ToUpper(status)
	}
}

func severityMarkdown(sev verdict.Severity) string {
	switch sev {
	case verdict.SeverityBlocker:
		return "❌ BLOCKER"
	case verdict.SeverityWarning:
		return "⚠️ WARN"
	default:
		return "ℹ️ INFO"
	}
}

func headlineMarkdown(line string) string {
	if strings.HasPrefix(line, "NOT TESTED LIVE.") {
		rest := strings.TrimSpace(strings.TrimPrefix(line, "NOT TESTED LIVE."))
		return "⚠️ **NOT TESTED LIVE.** " + rest
	}
	if strings.HasPrefix(line, "kind cluster ") || strings.HasPrefix(line, "This rehearsal used an empty cluster.") {
		return "ℹ️ " + line
	}
	return "⚠️ " + line
}

func formatDuration(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
	}
	return fmt.Sprintf("%dms", ms)
}

func mdCell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", `\|`)
}

func findingsSummary(findings []verdict.Finding) string {
	var blockers, warnings, infos int
	for _, f := range findings {
		switch f.Severity {
		case verdict.SeverityBlocker:
			blockers++
		case verdict.SeverityWarning:
			warnings++
		default:
			infos++
		}
	}
	var parts []string
	if blockers > 0 {
		parts = append(parts, pluralCount(blockers, "blocker", "blockers"))
	}
	if warnings > 0 {
		parts = append(parts, pluralCount(warnings, "warning", "warnings"))
	}
	if infos > 0 {
		parts = append(parts, fmt.Sprintf("%d info", infos))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d", len(findings))
	}
	return fmt.Sprintf("%d: %s", len(findings), strings.Join(parts, ", "))
}

func pluralCount(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func hasActiveBlocker(findings []verdict.Finding) bool {
	for _, f := range findings {
		if f.Severity == verdict.SeverityBlocker && !f.Ignored {
			return true
		}
	}
	return false
}

func writeMarkdownFooter(w io.Writer) {
	fmt.Fprint(w, "\n---\n\n<sub>Checked by [Stageproof](https://stageproof.github.io/stageproof/) · 0 AI calls</sub>\n")
}

func WriteMarkdown(w io.Writer, r *verdict.Report) {
	writeMarkdownBody(w, r)
	writeMarkdownFooter(w)
}

func writeMarkdownBody(w io.Writer, r *verdict.Report) {
	if r != nil {
		r.Findings = OrderFindings(r.Findings)
	}
	badge := string(r.Verdict)
	fmt.Fprintf(w, "## %s Stageproof Upgrade Gate: `%s` (exit `%d`)\n\n", verdictIcon(r.Verdict), badge, r.ExitCode)
	for _, line := range verdictHeadlines(r) {
		fmt.Fprintf(w, "> %s\n\n", headlineMarkdown(line))
	}

	k8s := "-"
	if r.TargetK8s != "" {
		k8s = "`" + mdCell(r.TargetK8s) + "`"
		if r.NextK8sEOL != "" {
			k8s += fmt.Sprintf(" (next `%s`)", mdCell(r.NextK8sEOL))
		}
	}
	checked := r.Checked
	if checked == "" {
		checked = verdict.CheckedFiles
	}
	compare := fmt.Sprintf("`%s` → `%s`", mdCell(r.BaseRef), mdCell(r.HeadRef))
	duration := fmt.Sprintf("`%s` · `0 AI calls`", formatDuration(r.DurationMs))
	if len(r.ValuesFiles) > 0 {
		values := "`" + mdCell(strings.Join(r.ValuesFiles, "`, `")) + "`"
		fmt.Fprintln(w, "| Compare | Values | Target K8s | Checked | Duration |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		fmt.Fprintf(w, "| %s | %s | %s | `%s` | %s |\n\n", compare, values, k8s, mdCell(checked), duration)
	} else {
		fmt.Fprintln(w, "| Compare | Target K8s | Checked | Duration |")
		fmt.Fprintln(w, "|---|---|---|---|")
		fmt.Fprintf(w, "| %s | %s | `%s` | %s |\n\n", compare, k8s, mdCell(checked), duration)
	}

	writeBriefMarkdown(w, r)

	fmt.Fprintln(w, "### Checks")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Check | Status | Summary |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, ck := range r.Checks {
		fmt.Fprintf(w, "| `%s` | %s | %s |\n", mdCell(ck.Name), checkStatusMarkdown(ck.Status), mdCell(ck.Summary))
	}

	if len(r.Findings) > 0 {
		openAttr := ""
		if hasActiveBlocker(r.Findings) {
			openAttr = " open"
		}
		fmt.Fprintf(w, "\n<details%s>\n<summary>📋 <b>Deterministic Findings (%s)</b></summary>\n\n", openAttr, findingsSummary(r.Findings))
		fmt.Fprintln(w, "| Severity | Rule | Target | Detail |")
		fmt.Fprintln(w, "|---|---|---|---|")
		for _, f := range r.Findings {
			loc := f.File
			if f.Resource != "" {
				if loc != "" {
					loc += " · " + f.Resource
				} else {
					loc = f.Resource
				}
			}
			target := "-"
			if loc != "" {
				target = "`" + mdCell(loc) + "`"
			}
			line := f.Message
			if f.Ignored {
				line += " Ignored until " + f.IgnoreExpires + ": " + oneLine(f.IgnoreReason)
			}
			fmt.Fprintf(w, "| %s | `%s` | %s | %s |\n", severityMarkdown(f.Severity), mdCell(f.RuleID), target, mdCell(line))
		}
		fmt.Fprintln(w, "\n</details>")
	}

	if len(r.Skipped) > 0 {
		fmt.Fprintln(w, "\n### ⏭️ Live rehearsal did not finish")
		fmt.Fprintln(w)
		for _, d := range r.Skipped {
			fmt.Fprintf(w, "- **`%s`**: %s\n", d.Code, d.Reason)
			if d.Remediation != "" {
				fmt.Fprintf(w, "  - %s\n", d.Remediation)
			}
		}
	}
}
