package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/stageproof/stageproof/internal/argocd"
	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

// mergeRequestSHAs is the head plus the commits this merge request adds.
// A saved live report records the chart commit. The file that carries it is
// often committed on top of that, so the gate has to recognize both.
func mergeRequestSHAs(base, head, headSHA string) map[string]bool {
	accept := map[string]bool{}
	if headSHA != "" {
		accept[headSHA] = true
	}
	if strings.TrimSpace(base) == "" || strings.TrimSpace(head) == "" {
		return accept
	}
	out, err := gitCommand("rev-list", "--max-count=30", head, "--not", base).Output()
	if err != nil {
		return accept
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			accept[line] = true
		}
	}
	return accept
}

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
	ssa := fs.Bool("server-side-apply", false, "Server-Side Apply enabled")
	live := fs.Bool("live", false, "Run live kind upgrade/rollback")
	requireRehearsal := fs.Bool("require-rehearsal", false, "Exit 2 unless the live rehearsal ran and passed")
	clusterName := fs.String("cluster-name", "", "kind cluster name. Default is unique per process.")
	timeout := fs.Duration("timeout", 0, "Readiness wait per step. Default is 10m.")
	format := fs.String("format", "text", "Output format: text | markdown | json")
	outPath := fs.String("out", "", "Optional JSON report output file")
	commentFile := fs.String("comment-file", "", "Also write the Markdown merge-request comment to this file")
	savedLive := fs.String("saved-live", "stageproof-live", "Directory of a live report CI cannot run. A report.json whose head_git is a commit on this merge request changes the gate verdict.")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
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

	seenCharts := make(map[string]bool)
	var chartDirs []string
	var argoFiles []string

	for _, line := range strings.Split(strings.TrimSpace(string(diffBytes)), "\n") {
		f := strings.TrimSpace(line)
		if f == "" {
			continue
		}
		if _, stErr := os.Stat(f); stErr != nil {
			continue
		}

		// 1. Check if file is an Argo CD Application YAML / .yaml.tmpl
		if strings.HasSuffix(f, ".yaml") || strings.HasSuffix(f, ".yml") || strings.HasSuffix(f, ".yaml.tmpl") {
			if _, _, aErr := argocd.ParseApplicationFile(f, templatePlaceholders()); aErr == nil || len(argocd.UnresolvedTokens(aErr)) > 0 {
				argoFiles = append(argoFiles, f)
				continue
			}
		}

		if app := configuredApplication(f); app != nil {
			key := "app:" + app.Name
			if !seenCharts[key] {
				seenCharts[key] = true
				argoFiles = append(argoFiles, key)
			}
			continue
		}

		// 3. Walk upward to find enclosing Helm `Chart.yaml`
		dir := filepath.Dir(f)
		for dir != "." && dir != "/" {
			if _, cErr := os.Stat(filepath.Join(dir, "Chart.yaml")); cErr == nil {
				// Walk up once more if inside `charts/<subchart>`
				parent := filepath.Dir(filepath.Dir(dir))
				if filepath.Base(filepath.Dir(dir)) == "charts" {
					if _, pErr := os.Stat(filepath.Join(parent, "Chart.yaml")); pErr == nil {
						dir = parent
					}
				}
				if !seenCharts[dir] {
					seenCharts[dir] = true
					chartDirs = append(chartDirs, dir)
				}
				break
			}
			dir = filepath.Dir(dir)
		}
	}

	if len(chartDirs) == 0 && len(argoFiles) == 0 {
		fmt.Printf("stageproof check-pr: no Helm charts or Argo CD Applications modified between %s and %s.\n", *currentGit, *newGit)
		return 0
	}

	worst := verdict.Proven
	note := func(st verdict.State) {
		if st.Rank() > worst.Rank() {
			worst = st
		}
	}

	// Flags that apply to every chart. --current-k8s is passed on only when the
	// caller typed it, so that each check can still read it from .stageproof.yaml.
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
			reports = append(reports, incompleteReport(*currentGit, label))
		}
		note(reports[len(reports)-1].Verdict)
	}

	cfg, _ := config.Load(".stageproof.yaml")
	for _, cd := range chartDirs {
		target := []string{"--new-chart", cd, "--current-git", *currentGit}
		valuesFiles := cfg.ValuesFilesFor(cd)
		if len(valuesFiles) == 0 {
			if vf := filepath.Join(cd, "values.yaml"); fileExists(vf) {
				valuesFiles = []string{vf}
			}
		}
		if len(valuesFiles) == 0 {
			runTarget(cd, target)
			continue
		}
		// Each values file is one deployment of the chart, so each gets its own check.
		for _, vf := range valuesFiles {
			runTarget(cd, append(append([]string(nil), target...), "-f", vf))
		}
	}

	for _, af := range argoFiles {
		if strings.HasPrefix(af, "app:") {
			app := configuredApplicationByName(strings.TrimPrefix(af, "app:"))
			if app == nil {
				reports = append(reports, incompleteReport(*currentGit, af))
				note(verdict.NotChecked)
				continue
			}
			newPath, oldPath, cleanup, sErr := synthesizeConfiguredApp(*app, *currentGit)
			if sErr != nil {
				fmt.Fprintf(os.Stderr, "stageproof check-pr: %s: %v\n", app.Name, sErr)
				reports = append(reports, incompleteReport(*currentGit, app.VersionFrom.File))
				note(verdict.NotChecked)
				continue
			}
			defer cleanup()
			runTarget(app.VersionFrom.File, []string{"--argocd-file", newPath, "--current-argocd-file", oldPath})
			continue
		}
		runTarget(af, []string{"--argocd-file", af, "--current-git", *currentGit})
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
	if extra, err := report.LoadSavedLive(*savedLive, mergeRequestSHAs(*currentGit, *newGit, headSHA)); err == nil {
		for _, r := range extra {
			reports = append(reports, r)
			// A rehearsal that could not run is shown. It does not fail the job.
			// A blocker in that report still does.
			if r.Verdict == verdict.Blocked {
				note(r.Verdict)
			}
		}
		if len(extra) > 0 && strings.EqualFold(*format, "text") {
			fmt.Println()
			for _, r := range extra {
				report.WriteTerminal(os.Stdout, r)
			}
		}
	}

	combined := &report.Combined{Verdict: worst, ExitCode: worst.ExitCode(), HeadGit: headSHA, Reports: reports}
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

func templatePlaceholders() map[string]string {
	cfg, err := config.Load(".stageproof.yaml")
	if err != nil {
		return nil
	}
	return cfg.TemplatePlaceholders
}

func configuredApplication(path string) *config.Application {
	cfg, err := config.Load(".stageproof.yaml")
	if err != nil {
		return nil
	}
	for i := range cfg.Applications {
		if cfg.Applications[i].CoversFile(path) {
			return &cfg.Applications[i]
		}
	}
	return nil
}

func configuredApplicationByName(name string) *config.Application {
	cfg, err := config.Load(".stageproof.yaml")
	if err != nil {
		return nil
	}
	for i := range cfg.Applications {
		if cfg.Applications[i].Name == name {
			return &cfg.Applications[i]
		}
	}
	return nil
}

// synthesizeConfiguredApp builds the candidate Application from the working
// tree and the current one from baseGitRef. The version and values files come
// from .stageproof.yaml, not from a built-in path.
func synthesizeConfiguredApp(app config.Application, baseGitRef string) (string, string, func(), error) {
	noop := func() {}
	newVer, err := versionFromFile(app.VersionFrom.File, app.VersionFrom.Regex, "")
	if err != nil {
		return "", "", noop, err
	}
	oldVer, err := versionFromFile(app.VersionFrom.File, app.VersionFrom.Regex, baseGitRef)
	if err != nil {
		return "", "", noop, err
	}
	newVals, err := readValues(app.ValuesFrom, "")
	if err != nil {
		return "", "", noop, err
	}
	oldVals, err := readValues(app.ValuesFrom, baseGitRef)
	if err != nil {
		return "", "", noop, err
	}
	tmp, err := os.MkdirTemp("", "sp-app-*")
	if err != nil {
		return "", "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	newPath := filepath.Join(tmp, "app-new.yaml")
	oldPath := filepath.Join(tmp, "app-current.yaml")
	if err := os.WriteFile(newPath, []byte(applicationYAML(app, newVer, newVals)), 0o644); err != nil {
		cleanup()
		return "", "", noop, err
	}
	if err := os.WriteFile(oldPath, []byte(applicationYAML(app, oldVer, oldVals)), 0o644); err != nil {
		cleanup()
		return "", "", noop, err
	}
	return newPath, oldPath, cleanup, nil
}

func versionFromFile(path, pattern, gitRef string) (string, error) {
	var raw []byte
	var err error
	if gitRef == "" {
		raw, err = os.ReadFile(path)
	} else {
		raw, err = readFileFromGitRef(gitRef, path)
	}
	if err != nil {
		return "", err
	}
	got, err := config.FirstCapture(string(raw), pattern)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return got, nil
}

func readValues(paths []string, gitRef string) (string, error) {
	var b strings.Builder
	for _, path := range paths {
		var raw []byte
		var err error
		if gitRef == "" {
			raw, err = os.ReadFile(path)
		} else {
			raw, err = readFileFromGitRef(gitRef, path)
		}
		if err != nil {
			return "", err
		}
		text := string(raw)
		for key, val := range templatePlaceholders() {
			text = strings.ReplaceAll(text, key, val)
		}
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

func applicationYAML(app config.Application, version, values string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: %s\nspec:\n  source:\n    repoURL: %s\n    chart: %s\n    targetRevision: %s\n    helm:\n      values: |\n", app.Name, app.RepoURL, app.Chart, version)
	for _, line := range strings.Split(values, "\n") {
		b.WriteString("        ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
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
