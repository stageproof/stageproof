package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/adapter"
	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

func counted(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// relabel returns paths with every entry that has a label replaced by it.
func relabel(paths []string, labels map[string]string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		if label, ok := labels[p]; ok {
			out[i] = label
		} else {
			out[i] = p
		}
	}
	return out
}

// inlineValuesLabel names the values written inside an Argo CD Application.
// A synthesized Application lives in the temp dir and has no path to show.
func inlineValuesLabel(argoFile string) string {
	if argoFile == "" || strings.HasPrefix(argoFile, os.TempDir()) {
		return "inline helm.values"
	}
	return "helm.values in " + argoFile
}

// flagWasSet reports whether the caller typed any of the named flags.
func flagWasSet(fs *flag.FlagSet, names ...string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		for _, n := range names {
			if f.Name == n {
				set = true
			}
		}
	})
	return set
}

func emitReport(rep *verdict.Report, format, outPath, commentPath string) {
	if outPath != "" {
		if f, err := os.Create(outPath); err == nil {
			_ = report.WriteJSON(f, rep)
			_ = f.Close()
		}
	}
	// The job log is colored text. The merge request comment stays Markdown,
	// because GitLab renders that note and does not render ANSI in comments.
	if commentPath != "" {
		if f, err := os.Create(commentPath); err == nil {
			report.WriteMarkdown(f, rep)
			_ = f.Close()
		}
	}
	switch strings.ToLower(format) {
	case "json":
		_ = report.WriteJSON(os.Stdout, rep)
	case "sarif":
		_ = report.WriteSARIF(os.Stdout, []*verdict.Report{rep}, version, string(rep.Verdict), rep.ExitCode)
	case "markdown", "md", "github", "gitlab-mr":
		report.WriteMarkdown(os.Stdout, rep)
	default:
		report.WriteTerminal(os.Stdout, rep)
	}
}

func defaultClusterName() string {
	return fmt.Sprintf("stageproof-%d", os.Getpid())
}

// gitCommand runs git in a checkout this process may not own.
// A container user and a runner-owned clone otherwise fail with "dubious ownership".
func gitCommand(args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	full = append(full, "-c", "safe.directory=*")
	full = append(full, args...)
	return exec.Command("git", full...)
}

func extractChartFromGitRef(ref, chartDir string) (string, func(), error) {
	repoRootBytes, err := gitCommand("rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", nil, fmt.Errorf("not inside a git repository: %w", err)
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	absChart, err := filepath.Abs(chartDir)
	if err != nil {
		return "", nil, err
	}
	relChart, err := filepath.Rel(repoRoot, absChart)
	if err != nil {
		return "", nil, err
	}

	tmpDir, err := os.MkdirTemp("", "stageproof-gitbase-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	gitCmd := gitCommand("-C", repoRoot, "archive", "--format=tar", ref, relChart)
	tarCmd := exec.Command("tar", "-x", "-C", tmpDir)
	pipe, err := gitCmd.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, err
	}
	tarCmd.Stdin = pipe
	if err := tarCmd.Start(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := gitCmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("git archive %s %s failed: %w", ref, relChart, err)
	}
	if err := tarCmd.Wait(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("tar extract failed: %w", err)
	}
	return filepath.Join(tmpDir, relChart), cleanup, nil
}

func readFileFromGitRef(ref, filePath string) ([]byte, error) {
	repoRootBytes, err := gitCommand("rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, err
	}
	repoRoot := strings.TrimSpace(string(repoRootBytes))
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return nil, err
	}
	relFile, err := filepath.Rel(repoRoot, absFile)
	if err != nil {
		return nil, err
	}
	return gitCommand("-C", repoRoot, "show", fmt.Sprintf("%s:%s", ref, filepath.ToSlash(relFile))).Output()
}

// runCheckPR inspects `git diff --name-only <current-git>...<new-git>` to automatically find
// every Helm chart directory or Argo CD Application manifest touched by a Renovate PR/MR,
// verifies each one, and returns the worst result: BLOCKED, then NOT CHECKED,
// then PASSED, then PROVEN. PASSED and PROVEN both exit 0.
func runCheckPR(args []string) int {
	return runCheckPRCtx(context.Background(), args)
}

func runCheckPRCtx(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("check-pr", flag.ContinueOnError)
	fs.Usage = printUsage
	currentGit := fs.String("current-git", "origin/main", "Base git ref/SHA of the PR/MR")
	newGit := fs.String("new-git", "HEAD", "Head git ref/SHA of the PR/MR")
	currentK8s := fs.String("current-k8s", "1.33", "Current cluster Kubernetes version")
	nextK8s := fs.String("next-k8s", "", "Next cluster Kubernetes version")
	apiServer := fs.String("api-server", "auto", "API server proof: auto, off, or require")
	apiServerDir := fs.String("api-server-dir", "", "Directory of kube-apiserver, etcd, and kubectl already on disk")
	valuesProof := fs.String("values-proof", "default", "Values proof: default, or full to render every user key")
	offline := fs.Bool("offline", false, "Do not contact container registries. Does not change --api-server.")
	clusterFacts := fs.String("cluster-facts", "", "JSON written by stageproof facts export. This path is not read from git.")
	helmCompare := fs.String("helm-compare", "", "Path to a second helm binary. Renders each chart with both.")
	ssa := fs.Bool("server-side-apply", false, "Server-Side Apply enabled")
	live := fs.Bool("live", false, "Run live kind upgrade/rollback")
	requireRehearsal := fs.Bool("require-rehearsal", false, "Exit 2 unless the live rehearsal ran and passed")
	clusterName := fs.String("cluster-name", "", "kind cluster name. Default is unique per process.")
	timeout := fs.Duration("timeout", 0, "Readiness wait per step. Default is 10m.")
	format := fs.String("format", "text", "Output format: text | markdown | json | sarif")
	outPath := fs.String("out", "", "Optional JSON report output file")
	commentFile := fs.String("comment-file", "", "Also write the Markdown merge-request comment to this file")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	switch *valuesProof {
	case "default", "full":
	default:
		fmt.Fprintf(os.Stderr, "error: --values-proof must be default or full\n")
		return 2
	}

	diffBytes, err := gitCommand("diff", "--name-only", *currentGit+"..."+*newGit).Output()
	if err != nil {
		diffBytes, err = gitCommand("diff", "--name-only", *currentGit, *newGit).Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "stageproof check-pr: git diff %s..%s failed: %v\n", *currentGit, *newGit, err)
			return 2
		}
	}

	// A deleted file is still a change. Stat would hide a template that the
	// pull request removed and nothing else in the chart touched.
	var changed []string
	for _, line := range strings.Split(strings.TrimSpace(string(diffBytes)), "\n") {
		f := strings.TrimSpace(line)
		if f != "" {
			changed = append(changed, f)
		}
	}
	baseCfg, configChanged, cfgErr := configFromBase(*currentGit, *newGit)
	if cfgErr != nil {
		fmt.Fprintf(os.Stderr, "stageproof check-pr: %v\n", cfgErr)
		return 2
	}
	ctx = config.WithCheck(ctx, baseCfg, configChanged)
	set := adapter.NewWithConfig(baseCfg)
	targets := set.Discover(".", changed)
	if len(targets) == 0 {
		machineOut := strings.EqualFold(*format, "json") || strings.EqualFold(*format, "sarif")
		if machineOut {
			fmt.Fprintf(os.Stderr, "stageproof check-pr: no Helm charts, Argo CD Applications, or Flux HelmReleases modified between %s and %s.\n", *currentGit, *newGit)
		} else {
			fmt.Printf("stageproof check-pr: no Helm charts, Argo CD Applications, or Flux HelmReleases modified between %s and %s.\n", *currentGit, *newGit)
		}
		if !configChanged {
			if strings.EqualFold(*format, "sarif") {
				_ = report.WriteSARIF(os.Stdout, nil, version, string(verdict.Passed), 0)
			} else if strings.EqualFold(*format, "json") {
				_ = report.WriteCombinedJSON(os.Stdout, &report.Combined{Verdict: verdict.Passed, ExitCode: 0, Reports: []*verdict.Report{}})
			}
			return 0
		}
		rep := &verdict.Report{
			Verdict:   verdict.Passed,
			ExitCode:  0,
			Adapter:   "helm",
			BaseRef:   *currentGit,
			HeadRef:   *newGit,
			Checked:   verdict.CheckedFiles,
			Timestamp: time.Now().UTC(),
			Checks: []verdict.CheckResult{{
				Name:    "settings",
				Status:  "warn",
				Summary: ".stageproof.yaml changed in this pull request.",
			}},
		}
		config.NoteConfigChanged(baseCfg, rep, time.Now().UTC())
		if strings.EqualFold(*format, "text") {
			report.WriteTerminal(os.Stdout, rep)
		}
		reports := []*verdict.Report{rep}
		if strings.EqualFold(*format, "sarif") {
			_ = report.WriteSARIF(os.Stdout, reports, version, string(rep.Verdict), rep.ExitCode)
		} else if strings.EqualFold(*format, "json") {
			_ = report.WriteJSON(os.Stdout, rep)
		} else if strings.EqualFold(*format, "markdown") || strings.EqualFold(*format, "md") {
			report.WriteMarkdown(os.Stdout, rep)
		}
		combined := &report.Combined{Verdict: rep.Verdict, ExitCode: rep.ExitCode, Reports: reports}
		if *outPath != "" {
			if f, cErr := os.Create(*outPath); cErr == nil {
				_ = report.WriteCombinedJSON(f, combined)
				_ = f.Close()
			}
		}
		if *commentFile != "" {
			if f, cErr := os.Create(*commentFile); cErr == nil {
				report.WriteCombinedMarkdown(f, combined)
				_ = f.Close()
			}
		}
		return rep.ExitCode
	}

	worst := verdict.Proven
	note := func(st verdict.State) {
		if st.Rank() > worst.Rank() {
			worst = st
		}
	}

	// Flags that apply to every chart. --current-k8s is passed on only when the
	// caller typed it. Otherwise each check reads .stageproof.yaml from the base ref.
	shared := []string{"--format", *format}
	if flagWasSet(fs, "current-k8s") {
		shared = append(shared, "--current-k8s", *currentK8s)
	}
	if *nextK8s != "" {
		shared = append(shared, "--next-k8s", *nextK8s)
	}
	if flagWasSet(fs, "api-server") {
		shared = append(shared, "--api-server", *apiServer)
	}
	if *apiServerDir != "" {
		shared = append(shared, "--api-server-dir", *apiServerDir)
	}
	if *valuesProof == "full" {
		shared = append(shared, "--values-proof", "full")
	}
	if *offline {
		shared = append(shared, "--offline")
	}
	if *clusterFacts != "" {
		shared = append(shared, "--cluster-facts", *clusterFacts)
	}
	if *helmCompare != "" {
		shared = append(shared, "--helm-compare", *helmCompare)
	}
	if *ssa {
		shared = append(shared, "--server-side-apply")
	}
	if *live {
		shared = append(shared, "--live")
	}
	if *requireRehearsal {
		shared = append(shared, "--require-rehearsal")
	}
	if *clusterName != "" {
		shared = append(shared, "--cluster-name", *clusterName)
	}
	if *timeout > 0 {
		shared = append(shared, "--timeout", timeout.String())
	}

	// Every check adds its report here. The file outputs are written once at the
	// end, so a pull request that changes several charts gets one comment.
	var reports []*verdict.Report
	runTarget := func(label string, target []string) {
		before := len(reports)
		if before > 0 && strings.EqualFold(*format, "text") {
			fmt.Println() // keep consecutive reports apart in the job log
		}
		_ = runCheckCollect(ctx, append(target, shared...), &reports)
		if len(reports) == before {
			rep := incompleteReport(*currentGit, label)
			if configChanged {
				config.NoteConfigChanged(baseCfg, rep, time.Now().UTC())
			}
			reports = append(reports, rep)
		}
		note(reports[len(reports)-1].Verdict)
	}

	for _, target := range targets {
		resolved, rErr := set.Resolve(ctx, target, *currentGit)
		if rErr != nil {
			fmt.Fprintf(os.Stderr, "stageproof check-pr: %s: %v\n", target.Path, rErr)
			rep := incompleteReport(*currentGit, target.Path)
			if configChanged {
				config.NoteConfigChanged(baseCfg, rep, time.Now().UTC())
			}
			reports = append(reports, rep)
			note(rep.Verdict)
			continue
		}
		if resolved.Cleanup != nil {
			defer resolved.Cleanup()
		}
		switch resolved.Adapter {
		case "argocd":
			args := []string{"--argocd-file", resolved.ArgoFile}
			if resolved.CurrentArgo != "" {
				args = append(args, "--current-argocd-file", resolved.CurrentArgo)
			} else {
				args = append(args, "--current-git", *currentGit)
			}
			runTarget(target.Path, args)
		case "flux":
			args := []string{"--flux-file", resolved.FluxFile, "--current-git", *currentGit}
			runTarget(target.Path, args)
		default:
			base := []string{"--new-chart", resolved.HeadChart, "--current-git", *currentGit}
			if len(resolved.Values) == 0 {
				runTarget(resolved.HeadChart, base)
				continue
			}
			// Each values file is one deployment of the chart, so each gets its own check.
			for _, vf := range resolved.Values {
				runTarget(resolved.HeadChart, append(append([]string(nil), base...), "-f", vf))
			}
		}
	}

	headSHA := ""
	if out, err := gitCommand("rev-parse", *newGit).Output(); err == nil {
		headSHA = strings.TrimSpace(string(out))
	}
	for _, r := range reports {
		if r.HeadGit == "" {
			r.HeadGit = headSHA
		}
	}

	combined := &report.Combined{Verdict: worst, ExitCode: worst.ExitCode(), HeadGit: headSHA, Reports: reports}
	if strings.EqualFold(*format, "sarif") {
		_ = report.WriteSARIF(os.Stdout, reports, version, string(worst), worst.ExitCode())
	}
	if *outPath != "" {
		if f, cErr := os.Create(*outPath); cErr == nil {
			_ = report.WriteCombinedJSON(f, combined)
			_ = f.Close()
		}
	}
	if *commentFile != "" {
		if f, cErr := os.Create(*commentFile); cErr == nil {
			report.WriteCombinedMarkdown(f, combined)
			_ = f.Close()
		}
	}
	return worst.ExitCode()
}

// incompleteReport stands in for a check that stopped before it had a report,
// for example because a chart could not be downloaded. Without it that chart
// would be missing from the pull request comment.
func unresolvedPlaceholderReport(label string, tokens []string) *verdict.Report {
	return &verdict.Report{
		Verdict:   verdict.NotChecked,
		ExitCode:  verdict.NotChecked.ExitCode(),
		Adapter:   "helm",
		BaseRef:   label,
		HeadRef:   label,
		Checked:   verdict.CheckedFiles,
		Timestamp: time.Now().UTC(),
		Checks: []verdict.CheckResult{{
			Name:    "chart source",
			Status:  "fail",
			Summary: "Unresolved placeholder in the chart repository, name, or version: " + strings.Join(tokens, ", "),
		}},
		Findings: []verdict.Finding{{
			RuleID:   "helm.config.unresolved_placeholder",
			Severity: verdict.SeverityWarning,
			File:     label,
			Message:  "Stageproof could not check this chart because a placeholder is still in the chart repository, name, or version.",
			Detail:   strings.Join(tokens, ", "),
		}},
	}
}

func incompleteReport(baseRef, target string) *verdict.Report {
	return &verdict.Report{
		Verdict:   verdict.NotChecked,
		ExitCode:  verdict.NotChecked.ExitCode(),
		Adapter:   "helm",
		BaseRef:   baseRef,
		HeadRef:   target,
		Checked:   verdict.CheckedFiles,
		Timestamp: time.Now().UTC(),
		Checks: []verdict.CheckResult{{
			Name:    "check",
			Status:  "fail",
			Summary: "Stageproof could not complete this check. The error is in the command output.",
		}},
	}
}

func configFromBase(baseRef, headRef string) (*config.Config, bool, error) {
	base, baseOK, err := gitBlob(baseRef, ".stageproof.yaml")
	if err != nil {
		return nil, false, err
	}
	head, headOK, err := gitBlob(headRef, ".stageproof.yaml")
	if err != nil {
		return nil, false, err
	}
	work, workErr := os.ReadFile(".stageproof.yaml")
	workOK := workErr == nil
	if workErr != nil && !os.IsNotExist(workErr) {
		return nil, false, workErr
	}
	changed := baseOK != headOK || !bytes.Equal(base, head) || baseOK != workOK || !bytes.Equal(base, work)
	if !baseOK {
		return &config.Config{}, changed, nil
	}
	cfg, err := config.LoadBytes(baseRef+":.stageproof.yaml", base)
	if err != nil {
		return nil, false, err
	}
	return cfg, changed, nil
}

func gitBlob(ref, name string) ([]byte, bool, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, false, fmt.Errorf("git ref is empty")
	}
	if err := gitCommand("rev-parse", "--verify", "--quiet", ref+"^{commit}").Run(); err != nil {
		return nil, false, fmt.Errorf("git ref %s is not a commit", ref)
	}
	out, err := gitCommand("show", ref+":"+name).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return out, true, nil
}

// chartRefLabel renders chart@version. A synthesized Application under the
// temp dir is not a source path, so it is left off the merge request comment.
func chartRefLabel(chart, revision, source string) string {
	label := chart + "@" + revision
	if source == "" || strings.HasPrefix(source, os.TempDir()) {
		return label
	}
	return label + " (" + source + ")"
}
