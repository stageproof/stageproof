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
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/rehearse"
	"github.com/stageproof/stageproof/internal/report"
	"github.com/stageproof/stageproof/internal/verdict"
)

// version is the release tag. A release build replaces it with the tag.
var version = "dev"

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(val string) error {
	for _, part := range strings.Split(val, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			*m = append(*m, part)
		}
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "check-pr", "check-mr":
		os.Exit(runCheckPR(os.Args[2:]))
	case "version", "--version", "-v":
		fmt.Printf("stageproof %s (Apache-2.0)\n", version)
		os.Exit(0)
	case "help", "--help", "-h":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `stageproof - deterministic Helm and Kubernetes upgrade checks

Usage:
  stageproof init     [--dir .] [--out .stageproof.yaml]
  stageproof check    --current-chart <dir> --new-chart <dir> [flags]
  stageproof check    --argocd-file <app.yaml> --current-git <git-ref> [flags]
  stageproof check-pr --current-git <base-sha> [--new-git HEAD] [flags]

Results:
  PROVEN       exit 0   file checks passed and the upgrade was rehearsed
  PASSED       exit 0   file checks passed; the upgrade was not rehearsed
  BLOCKED      exit 1   the upgrade has a problem the report names
  NOT CHECKED  exit 2   the check did not finish, or --require-rehearsal was set and no rehearsal ran

Chart, Argo CD & Values:
  --current-chart <dir>    Currently deployed Helm chart directory (before upgrade)
  --new-chart <dir>        New candidate Helm chart directory to verify (after upgrade)
  --argocd-file <file>     Argo CD Application YAML / .yaml.tmpl (auto-pulls chart + values + SSA)
  --current-argocd-file <file>
                           Previously deployed Argo CD Application, compared with --argocd-file
  --current-git <ref>      Extract current chart / Argo CD file from git ref (e.g. origin/main)
  -f, --values <file>      Your values.yaml file(s) (repeatable)
  --base-values <file>     Optional separate values.yaml for --current-chart (defaults to -f)
  --set <k=v>              Explicit Helm --set override(s) (repeatable)

Kubernetes & GitOps Environment:
  --current-k8s <ver>      Current cluster Kubernetes version (default "1.33")
  --next-k8s <ver>         Next Kubernetes version you plan to upgrade to (e.g. "1.34")
  --server-side-apply      Server-Side Apply is enabled in Argo CD / Flux / Helm v4 (suppresses >256KiB CRD warning)

Live Cluster Test (temporary kind cluster on this machine):
  --live                   Install the current chart, upgrade to the new chart, then roll back. Nothing is committed. No existing cluster is changed.
  --require-rehearsal      Exit 2 (NOT CHECKED) unless that rehearsal ran and passed
  --keep-cluster           Keep kind cluster running after check for fast repeat runs
  --cluster-name <name>    Name of kind cluster (default is unique per process)
  --crd-file <path/url>    Extra CRD YAML file or URL to pre-install in kind (repeatable)
  --helm-test              Also run 'helm test' hooks after upgrade
  --timeout <dur>          Readiness wait timeout per step (default 10m)

Output:
  --format <fmt>           text | markdown | json (default "text")
  --out <file>             Save JSON evidence report to file
  --comment-file <file>    Also write the Markdown merge-request comment (job log stays colored text)

Examples:
  # 1. Compare current chart vs new chart with your prod values + check K8s 1.33 & 1.34
  stageproof check --current-chart ./charts/argo-v5 --new-chart ./charts/argo-v10 -f values-prod.yaml --current-k8s 1.33 --next-k8s 1.34

  # 2. Check a pull request from its git diff. Exit 0 is green. Exit 1 and 2 fail CI.
  stageproof check-pr --current-git "$CI_MERGE_REQUEST_DIFF_BASE_SHA" --current-k8s 1.35 --next-k8s 1.36 --comment-file comment.md
`)
}

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", ".", "root directory to scan")
	out := fs.String("out", ".stageproof.yaml", "config file output path")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	cfg, err := config.Discover(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stageproof init failed: %v\n", err)
		return 2
	}
	if err := config.Write(*out, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "write %s failed: %v\n", *out, err)
		return 2
	}
	fmt.Printf("Wrote %s (discovered %s, currentKubernetes=%s, nextKubernetes=%s)\n",
		*out, counted(len(cfg.Charts), "Helm chart", "Helm charts"), cfg.CurrentKubernetes, cfg.NextKubernetes)
	return 0
}

func runCheck(args []string) int {
	return runCheckCollect(args, nil)
}

// runCheckCollect runs one check. When collected is not nil the report is also
// appended to it, so that check-pr can combine the reports of several charts.
func runCheckCollect(args []string, collected *[]*verdict.Report) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.Usage = printUsage

	// Primary matched flags: --current-* / --new-* + --argocd-file + --base-values
	currentChart := fs.String("current-chart", "", "Currently deployed Helm chart directory")
	newChart := fs.String("new-chart", "", "New candidate Helm chart directory")
	argoFile := fs.String("argocd-file", "", "Argo CD Application YAML or .yaml.tmpl file")
	currentArgo := fs.String("current-argocd-file", "", "Previously deployed Argo CD Application file, compared with --argocd-file")
	currentGit := fs.String("current-git", "", "Git ref to extract --current-chart / --argocd-file from")

	var valuesFiles multiFlag
	var baseValuesFiles multiFlag
	var setValues multiFlag
	var crdFiles multiFlag
	fs.Var(&valuesFiles, "values", "User values.yaml file(s)")
	fs.Var(&valuesFiles, "f", "User values.yaml file(s) (shorthand)")
	fs.Var(&baseValuesFiles, "base-values", "Separate values.yaml file(s) for --current-chart")
	fs.Var(&setValues, "set", "Explicit --set key=val override(s)")
	fs.Var(&crdFiles, "crd-file", "Extra CRD file/URL for live test")

	currentK8s := fs.String("current-k8s", "1.33", "Current cluster Kubernetes version")
	nextK8s := fs.String("next-k8s", "", "Next cluster Kubernetes version")
	ssa := fs.Bool("server-side-apply", false, "Server-Side Apply is enabled in Argo CD / Flux / Helm")
	applyMode := fs.String("apply-mode", "", "Apply mode: client | ssa")

	live := fs.Bool("live", false, "Run live install -> upgrade -> rollback in ephemeral kind cluster")
	requireRehearsal := fs.Bool("require-rehearsal", false, "Exit 2 unless the live rehearsal ran and passed")
	keepCluster := fs.Bool("keep-cluster", false, "Keep kind cluster after run")
	clusterName := fs.String("cluster-name", defaultClusterName(), "kind cluster name. Default is unique to this process so parallel rehearsals do not collide.")
	runHelmTest := fs.Bool("helm-test", false, "Run helm test hook after upgrade")
	timeout := fs.Duration("timeout", rehearse.DefaultReadinessTimeout, "Readiness wait per step. Heavy charts need several minutes.")
	format := fs.String("format", "text", "Output format: text | markdown | json")
	outPath := fs.String("out", "", "Optional JSON report output file")
	commentFile := fs.String("comment-file", "", "Also write the Markdown merge-request comment to this file")

	// Backwards-compatible aliases
	aliasTo := fs.String("to", "", "Alias for --new-chart")
	aliasFrom := fs.String("from", "", "Alias for --current-chart")
	aliasChart := fs.String("chart", "", "Alias for --new-chart")
	aliasBaseChart := fs.String("base-chart", "", "Alias for --current-chart")
	aliasFromGit := fs.String("from-git", "", "Alias for --current-git")
	aliasGitBase := fs.String("git-base", "", "Alias for --current-git")
	fs.Var(&baseValuesFiles, "from-values", "Alias for --base-values")
	fs.Var(&crdFiles, "crd-bundle", "Alias for --crd-file")
	aliasCurrentK8sFull := fs.String("current-kubernetes", "", "Alias for --current-k8s")
	aliasNextK8sFull := fs.String("next-kubernetes", "", "Alias for --next-k8s")
	aliasTargetK8s := fs.String("target-k8s", "", "Alias for --current-k8s")
	aliasK8sVersion := fs.String("k8s-version", "", "Alias for --current-k8s")
	aliasK8sNext := fs.String("k8s-next", "", "Alias for --next-k8s")
	legacyRehearse := fs.Bool("rehearse", false, "Alias for --live")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	headDir := firstNonEmpty(*newChart, *aliasTo, *aliasChart)
	baseDir := firstNonEmpty(*currentChart, *aliasFrom, *aliasBaseChart)
	gitRef := firstNonEmpty(*currentGit, *aliasFromGit, *aliasGitBase)
	effK8s := firstNonEmpty(*aliasCurrentK8sFull, *aliasTargetK8s, *aliasK8sVersion, *currentK8s)
	effNextK8s := firstNonEmpty(*nextK8s, *aliasNextK8sFull, *aliasK8sNext)
	effLive := *live || *legacyRehearse
	effRequire := *requireRehearsal
	effSSA := *ssa || strings.EqualFold(*applyMode, "ssa") || strings.EqualFold(*applyMode, "server-side")

	emit := func(rep *verdict.Report) {
		emitReport(rep, *format, *outPath, *commentFile)
		if collected != nil {
			*collected = append(*collected, rep)
		}
	}

	if cfg, cfgErr := config.Load(".stageproof.yaml"); cfgErr == nil {
		// --current-k8s has a default, so only a flag the caller typed overrides the file.
		if !flagWasSet(fs, "current-k8s", "current-kubernetes", "target-k8s", "k8s-version") && cfg.CurrentKubernetes != "" {
			effK8s = cfg.CurrentKubernetes
		}
		if effNextK8s == "" && cfg.NextKubernetes != "" {
			effNextK8s = cfg.NextKubernetes
		}
		if !effSSA && cfg.ServerSideApply {
			effSSA = true
		}
		if !effLive && cfg.LiveTest {
			effLive = true
		}
		if !effRequire && !flagWasSet(fs, "require-rehearsal") && cfg.RequireRehearsal {
			effRequire = true
		}
		if len(crdFiles) == 0 && len(cfg.CRDFiles) > 0 {
			crdFiles = append(crdFiles, cfg.CRDFiles...)
		}
	}

	if headDir == "" && *argoFile == "" {
		fmt.Fprintln(os.Stderr, "error: --new-chart <dir> or --argocd-file <file> is required")
		fmt.Fprintln(os.Stderr, "Run `stageproof check --help` for examples.")
		return 2
	}

	tStart := time.Now()
	ctx := context.Background()

	resolvedBaseDir := baseDir
	effectiveValues := []string(valuesFiles)
	baseVals := []string(baseValuesFiles)
	baseLabel := "(single-chart check)"
	headLabel := headDir
	// Values written inside an Argo CD Application are checked from a temporary
	// file. Reports name where the values came from, not that file.
	valuesLabel := map[string]string{}

	// Argo CD Application / .yaml.tmpl mode: auto-pull remote Helm chart(s), extract inline values & syncOptions!
	if *argoFile != "" {
		newSpec, aErr := argocd.ParseApplicationFile(*argoFile)
		if aErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing --argocd-file %s: %v\n", *argoFile, aErr)
			return 2
		}
		tmpPull, mErr := os.MkdirTemp("", "stageproof-argopull-*")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "tempdir error: %v\n", mErr)
			return 2
		}
		defer os.RemoveAll(tmpPull)

		pulledNew, pErr := argocd.PullChart(ctx, newSpec.RepoURL, newSpec.Chart, newSpec.TargetRevision, filepath.Join(tmpPull, "new"))
		if pErr != nil {
			fmt.Fprintf(os.Stderr, "error pulling candidate chart %s@%s: %v\n", newSpec.Chart, newSpec.TargetRevision, pErr)
			return 2
		}
		headDir = pulledNew
		headLabel = chartRefLabel(newSpec.Chart, newSpec.TargetRevision, *argoFile)
		if newSpec.ServerSideApply {
			effSSA = true
		}
		setValues = append(setValues, newSpec.SetParams...)
		if strings.TrimSpace(newSpec.ValuesYAML) != "" {
			vfPath := filepath.Join(tmpPull, "argocd-inline-values.yaml")
			_ = os.WriteFile(vfPath, []byte(newSpec.ValuesYAML), 0o644)
			effectiveValues = append(effectiveValues, vfPath)
			valuesLabel[vfPath] = inlineValuesLabel(*argoFile)
		}

		var oldSpec *argocd.AppSpec
		oldLabel := ""
		if *currentArgo != "" {
			parsed, oErr := argocd.ParseApplicationFile(*currentArgo)
			if oErr != nil {
				fmt.Fprintf(os.Stderr, "error parsing --current-argocd-file %s: %v\n", *currentArgo, oErr)
				return 2
			}
			oldSpec = parsed
			oldLabel = *currentArgo
		} else if gitRef != "" {
			oldBytes, gErr := readFileFromGitRef(gitRef, *argoFile)
			if gErr == nil {
				if parsed, oErr := argocd.ParseApplicationBytes(oldBytes, gitRef+":"+*argoFile); oErr == nil {
					oldSpec = parsed
					oldLabel = gitRef
				}
			}
		}
		if oldSpec != nil {
			pulledOld, poErr := argocd.PullChart(ctx, oldSpec.RepoURL, oldSpec.Chart, oldSpec.TargetRevision, filepath.Join(tmpPull, "current"))
			if poErr != nil {
				fmt.Fprintf(os.Stderr, "error pulling current chart %s@%s: %v\n", oldSpec.Chart, oldSpec.TargetRevision, poErr)
				return 2
			}
			resolvedBaseDir = pulledOld
			baseLabel = chartRefLabel(oldSpec.Chart, oldSpec.TargetRevision, oldLabel)
			if strings.TrimSpace(oldSpec.ValuesYAML) != "" && len(baseVals) == 0 {
				bvfPath := filepath.Join(tmpPull, "argocd-base-values.yaml")
				_ = os.WriteFile(bvfPath, []byte(oldSpec.ValuesYAML), 0o644)
				baseVals = []string{bvfPath}
			}
		}
	} else {
		if resolvedBaseDir == "" && gitRef != "" {
			tmpGitDir, cleanup, gErr := extractChartFromGitRef(gitRef, headDir)
			if gErr != nil {
				fmt.Fprintf(os.Stderr, "error extracting --current-git %s: %v\n", gitRef, gErr)
				return 2
			}
			defer cleanup()
			resolvedBaseDir = tmpGitDir
		}
		if gitRef != "" {
			baseLabel = gitRef + ":" + headDir
		} else if resolvedBaseDir != "" {
			baseLabel = resolvedBaseDir
		}
	}

	if len(baseVals) == 0 {
		baseVals = effectiveValues
	}
	if yml, yErr := helm.AssignmentsYAML(setValues); yErr == nil && strings.TrimSpace(yml) != "" {
		dir, dErr := os.MkdirTemp("", "stageproof-set-*")
		if dErr == nil {
			defer os.RemoveAll(dir)
			setPath := filepath.Join(dir, "parameters.yaml")
			if wErr := os.WriteFile(setPath, []byte(yml), 0o644); wErr == nil {
				effectiveValues = append(effectiveValues, setPath)
				valuesLabel[setPath] = "helm.parameters"
			}
		}
	}

	rep := &verdict.Report{
		Adapter:     "helm",
		TargetK8s:   effK8s,
		NextK8sEOL:  effNextK8s,
		BaseRef:     baseLabel,
		HeadRef:     headLabel,
		ValuesFiles: relabel(effectiveValues, valuesLabel),
		Checked:     verdict.CheckedFiles,
		Timestamp:   time.Now().UTC(),
	}

	// Pre-scan `required` `.Values.*` AST calls
	placeholders, newReqBlockers := helm.PlanRequiredPlaceholders(resolvedBaseDir, headDir, effectiveValues, setValues)
	if len(newReqBlockers) > 0 {
		rep.Findings = append(rep.Findings, newReqBlockers...)
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    "required values gate",
			Status:  "fail",
			Summary: fmt.Sprintf("%s introduced by the upgrade", counted(len(newReqBlockers), "new unset `required` value", "new unset `required` values")),
		})
		rep.DurationMs = time.Since(tStart).Milliseconds()
		rep.Finalize(false, false, effRequire)
		emit(rep)
		return rep.ExitCode
	}
	if len(placeholders) > 0 {
		// A template can mention required on a path the current values never
		// reach. If the chart renders without a placeholder, the key is not a
		// secret the chart refuses to start without.
		if _, trialErr := helm.RenderChart(ctx, helm.RenderOptions{
			ChartDir:    headDir,
			ValuesFiles: effectiveValues,
			SetValues:   setValues,
			KubeVersion: effK8s,
		}); trialErr == nil {
			placeholders = nil
		}
	}
	if len(placeholders) > 0 {
		var keysOnly []string
		for _, kv := range placeholders {
			keysOnly = append(keysOnly, strings.SplitN(kv, "=", 2)[0])
		}
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    "required values shim",
			Status:  "warn",
			Summary: fmt.Sprintf("the chart does not render unless %s is set: %s", counted(len(keysOnly), "this value", "these values"), strings.Join(keysOnly, ", ")),
		})
	}

	// 1. Render `--from` (if provided) and `--to` charts with target K8s version
	var baseObjs []k8sschema.ManifestObject
	if resolvedBaseDir != "" {
		baseYAML, bErr := helm.RenderChart(ctx, helm.RenderOptions{
			ChartDir:    resolvedBaseDir,
			ValuesFiles: baseVals,
			SetValues:   setValues,
			SetStrings:  placeholders,
			KubeVersion: effK8s,
		})
		if bErr != nil {
			rep.Checks = append(rep.Checks, verdict.CheckResult{
				Name:    "baseline helm template",
				Status:  "fail",
				Summary: bErr.Error(),
			})
			rep.DurationMs = time.Since(tStart).Milliseconds()
			rep.Finalize(false, true, effRequire)
			emit(rep)
			return rep.ExitCode
		}
		var pErr error
		baseObjs, pErr = k8sschema.ParseManifestStream(baseYAML)
		if pErr != nil {
			rep.Checks = append(rep.Checks, verdict.CheckResult{Name: "baseline manifest parse", Status: "fail", Summary: pErr.Error()})
			rep.DurationMs = time.Since(tStart).Milliseconds()
			rep.Finalize(false, true, effRequire)
			emit(rep)
			return rep.ExitCode
		}
	}

	headYAML, hErr := helm.RenderChart(ctx, helm.RenderOptions{
		ChartDir:    headDir,
		ValuesFiles: effectiveValues,
		SetValues:   setValues,
		SetStrings:  placeholders,
		KubeVersion: effK8s,
	})
	if hErr != nil {
		rep.Findings = append(rep.Findings, verdict.Finding{
			RuleID:   "helm.template.render_failed",
			Severity: verdict.SeverityBlocker,
			File:     headDir,
			Message:  "Candidate chart (`--to`) failed `helm template` rendering.",
			Detail:   hErr.Error(),
		})
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    "helm template",
			Status:  "fail",
			Summary: "render failed",
		})
		rep.DurationMs = time.Since(tStart).Milliseconds()
		rep.Finalize(false, false, effRequire)
		emit(rep)
		return rep.ExitCode
	}

	headObjs, hpErr := k8sschema.ParseManifestStream(headYAML)
	if hpErr != nil {
		rep.Findings = append(rep.Findings, verdict.Finding{
			RuleID:   "helm.template.invalid_yaml",
			Severity: verdict.SeverityBlocker,
			File:     headDir,
			Message:  hpErr.Error(),
		})
		rep.DurationMs = time.Since(tStart).Milliseconds()
		rep.Finalize(false, false, effRequire)
		emit(rep)
		return rep.ExitCode
	}
	rep.Checks = append(rep.Checks, verdict.CheckResult{
		Name:    "helm template",
		Status:  "pass",
		Summary: fmt.Sprintf("rendered %s cleanly", counted(len(headObjs), "Kubernetes object", "Kubernetes objects")),
	})

	// 2. Values AST Orphaned / Dropped Key Analysis
	if len(effectiveValues) > 0 {
		valFindings, _, _, vErr := helm.DiffValuesConsumption(resolvedBaseDir, headDir, effectiveValues)
		if vErr != nil {
			rep.Findings = append(rep.Findings, verdict.Finding{
				RuleID:   "helm.values.parse_error",
				Severity: verdict.SeverityBlocker,
				Message:  vErr.Error(),
			})
			rep.Checks = append(rep.Checks, verdict.CheckResult{Name: "values.yaml AST", Status: "fail", Summary: vErr.Error()})
		} else {
			for i := range valFindings {
				if label, ok := valuesLabel[valFindings[i].File]; ok {
					valFindings[i].File = label
				}
			}
			rep.Findings = append(rep.Findings, valFindings...)
			dropped := countBlockers(valFindings)
			if dropped > 0 {
				rep.Checks = append(rep.Checks, verdict.CheckResult{
					Name:    "values.yaml AST",
					Status:  "fail",
					Summary: fmt.Sprintf("%s across %s", counted(dropped, "silently dropped key", "silently dropped keys"), counted(len(effectiveValues), "overlay", "overlays")),
				})
			} else {
				rep.Checks = append(rep.Checks, verdict.CheckResult{
					Name:    "values.yaml AST",
					Status:  "pass",
					Summary: fmt.Sprintf("every user key in %s is still read", counted(len(effectiveValues), "overlay", "overlays")),
				})
			}
		}
	} else {
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    "values.yaml AST",
			Status:  "pass",
			Summary: "no custom -f overlay passed (default chart values verified)",
		})
	}

	// 3. Target Kubernetes API Compatibility, >256KiB CRD Limit & Immutable Selector Diff
	compatFindings := k8sschema.CheckCompatibility(headObjs, effK8s, effNextK8s, effSSA)
	if len(baseObjs) > 0 {
		compatFindings = append(compatFindings, k8sschema.DiffUpgradeSafety(baseObjs, headObjs)...)
	}
	if resolvedBaseDir != "" {
		signals := helm.UpgradeSignals(resolvedBaseDir, headDir, baseObjs, headObjs)
		if len(signals) > 0 {
			rep.Findings = append(rep.Findings, signals...)
			rep.Checks = append(rep.Checks, verdict.CheckResult{
				Name:    "upgrade signals",
				Status:  "warn",
				Summary: counted(len(signals), "named change", "named changes"),
			})
		}
	}
	rep.Findings = append(rep.Findings, compatFindings...)
	k8sBlockers := countBlockers(compatFindings)
	if k8sBlockers > 0 {
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    fmt.Sprintf("k8s %s compatibility", effK8s),
			Status:  "fail",
			Summary: counted(k8sBlockers, "removed API or immutable upgrade blocker", "removed API or immutable upgrade blockers"),
		})
	} else {
		summary := fmt.Sprintf("0 removed APIs or selector breaks on K8s %s", effK8s)
		if effSSA {
			summary += " (SSA mode)"
		}
		if len(compatFindings) > 0 {
			summary = fmt.Sprintf("0 blockers (%s)", counted(len(compatFindings), "deprecation, EOL, or CRD warning", "deprecation, EOL, or CRD warnings"))
		}
		rep.Checks = append(rep.Checks, verdict.CheckResult{
			Name:    fmt.Sprintf("k8s %s compatibility", effK8s),
			Status:  "pass",
			Summary: summary,
		})
	}

	// 4. Optional live rehearsal: install, upgrade, and roll back in a temporary kind cluster.
	runtimeOK := false
	inconclusive := false
	if effLive && countBlockers(rep.Findings) == 0 {
		extCRDs := k8sschema.DetectExternalCRDs(append(baseObjs, headObjs...))
		outcome, rErr := rehearse.RunKindRehearsal(ctx, rehearse.KindOptions{
			KubernetesVersion: effK8s,
			ClusterName:       *clusterName,
			KeepCluster:       *keepCluster,
			BaseChartDir:      resolvedBaseDir,
			HeadChartDir:      headDir,
			BaseValues:        baseVals,
			HeadValues:        effectiveValues,
			SetValues:         setValues,
			SetStrings:        placeholders,
			ExternalCRDs:      extCRDs,
			ExtraCRDFiles:     crdFiles,
			Timeout:           *timeout,
			RunHelmTest:       *runHelmTest,
		})
		if rErr != nil {
			inconclusive = true
			rep.Checks = append(rep.Checks, verdict.CheckResult{Name: "kind live test", Status: "fail", Summary: rErr.Error()})
		} else {
			if outcome.Rehearsed {
				rep.Checked = verdict.CheckedFilesAndLive
			}
			rep.Checks = append(rep.Checks, outcome.Checks...)
			rep.Findings = append(rep.Findings, outcome.Findings...)
			rep.Skipped = append(rep.Skipped, outcome.Degraded...)
			rep.ExcludedChecks = append(rep.ExcludedChecks, outcome.ExcludedChecks...)
			runtimeOK = outcome.RuntimeSucceeded
			inconclusive = outcome.Inconclusive
			if outcome.ClusterName != "" {
				rep.Cluster = outcome.ClusterName
			}
		}
	}
	// --live was asked for. A file check that passed while the chart never
	// came up is not PASSED. PASSED stays for a run that did not attempt --live.
	if effLive && !runtimeOK {
		inconclusive = true
	}

	rep.DurationMs = time.Since(tStart).Milliseconds()
	rep.Finalize(runtimeOK, inconclusive, effRequire)
	emit(rep)
	return rep.ExitCode
}

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

func countBlockers(findings []verdict.Finding) int {
	n := 0
	for _, f := range findings {
		if f.Severity == verdict.SeverityBlocker {
			n++
		}
	}
	return n
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
	fs := flag.NewFlagSet("check-pr", flag.ContinueOnError)
	fs.Usage = printUsage
	currentGit := fs.String("current-git", "origin/main", "Base git ref/SHA of the PR/MR")
	newGit := fs.String("new-git", "HEAD", "Head git ref/SHA of the PR/MR")
	currentK8s := fs.String("current-k8s", "1.33", "Current cluster Kubernetes version")
	nextK8s := fs.String("next-k8s", "", "Next cluster Kubernetes version")
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
			if _, aErr := argocd.ParseApplicationFile(f); aErr == nil {
				argoFiles = append(argoFiles, f)
				continue
			}
		}

		// 2. Special-case Terraform modules calling argo-cd chart (`terraform/modules/argo-cd/*`)
		if strings.HasPrefix(f, "terraform/modules/argo-cd/") {
			if !seenCharts["__tf_argocd__"] {
				seenCharts["__tf_argocd__"] = true
				argoFiles = append(argoFiles, "__TF_ARGOCD__")
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
		_ = runCheckCollect(append(target, shared...), &reports)
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
		if af == "__TF_ARGOCD__" {
			newPath, oldPath, cleanup, sErr := synthesizeTerraformArgoApps(*currentGit)
			if sErr != nil {
				fmt.Fprintf(os.Stderr, "stageproof check-pr: terraform argo-cd: %v\n", sErr)
				reports = append(reports, incompleteReport(*currentGit, "terraform/modules/argo-cd"))
				note(verdict.NotChecked)
				continue
			}
			defer cleanup()
			runTarget("terraform/modules/argo-cd", []string{"--argocd-file", newPath, "--current-argocd-file", oldPath})
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

// synthesizeTerraformArgoApps builds the candidate Application from the working
// tree and the current Application from baseGitRef. The chart version lives in
// terraform/modules/argo-cd/variables.tf, which is not an Argo CD manifest, so
// git show of a temp file cannot recover the old revision.
func synthesizeTerraformArgoApps(baseGitRef string) (string, string, func(), error) {
	const varsPath = "terraform/modules/argo-cd/variables.tf"
	const valsPath = "terraform/modules/argo-cd/values.yaml.tmpl"
	noop := func() {}

	newVars, err := os.ReadFile(varsPath)
	if err != nil {
		return "", "", noop, err
	}
	newVals, err := os.ReadFile(valsPath)
	if err != nil {
		return "", "", noop, err
	}
	newVer := chartVersionFromTF(string(newVars))
	if newVer == "" {
		return "", "", noop, fmt.Errorf("could not parse chart_version in %s", varsPath)
	}

	oldVars, err := readFileFromGitRef(baseGitRef, varsPath)
	if err != nil {
		return "", "", noop, fmt.Errorf("read %s from %s: %w", varsPath, baseGitRef, err)
	}
	oldVals, err := readFileFromGitRef(baseGitRef, valsPath)
	if err != nil {
		return "", "", noop, fmt.Errorf("read %s from %s: %w", valsPath, baseGitRef, err)
	}
	oldVer := chartVersionFromTF(string(oldVars))
	if oldVer == "" {
		return "", "", noop, fmt.Errorf("could not parse chart_version in %s at %s", varsPath, baseGitRef)
	}

	tmp, err := os.MkdirTemp("", "sp-tf-argo-*")
	if err != nil {
		return "", "", noop, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	newPath := filepath.Join(tmp, "argocd-new.yaml")
	oldPath := filepath.Join(tmp, "argocd-current.yaml")
	if err := os.WriteFile(newPath, []byte(terraformArgoAppYAML(newVer, string(newVals))), 0o644); err != nil {
		cleanup()
		return "", "", noop, err
	}
	if err := os.WriteFile(oldPath, []byte(terraformArgoAppYAML(oldVer, string(oldVals))), 0o644); err != nil {
		cleanup()
		return "", "", noop, err
	}
	return newPath, oldPath, cleanup, nil
}

func terraformArgoAppYAML(version, values string) string {
	var b strings.Builder
	b.WriteString("apiVersion: argoproj.io/v1alpha1\nkind: Application\nmetadata:\n  name: argo-cd\nspec:\n  source:\n    repoURL: https://argoproj.github.io/argo-helm\n    chart: argo-cd\n    targetRevision: ")
	b.WriteString(version)
	b.WriteString("\n    helm:\n      values: |\n")
	for _, line := range strings.Split(values, "\n") {
		b.WriteString("        ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// chartVersionFromTF returns the default of variable "chart_version".
// The first `default` in the file is not used: other variables and the
// description string sit above that default.
// chartRefLabel renders chart@version. A synthesized Application under the
// temp dir is not a source path, so it is left off the merge request comment.
func chartRefLabel(chart, revision, source string) string {
	label := chart + "@" + revision
	if source == "" || strings.HasPrefix(source, os.TempDir()) {
		return label
	}
	return label + " (" + source + ")"
}

func chartVersionFromTF(tf string) string {
	const marker = `variable "chart_version"`
	i := strings.Index(tf, marker)
	if i < 0 {
		return ""
	}
	rest := tf[i+len(marker):]
	if j := strings.Index(rest, "\nvariable "); j >= 0 {
		rest = rest[:j]
	}
	def := strings.Index(rest, "default")
	if def < 0 {
		return ""
	}
	rest = rest[def:]
	q1 := strings.IndexByte(rest, '"')
	if q1 < 0 {
		return ""
	}
	q2 := strings.IndexByte(rest[q1+1:], '"')
	if q2 < 0 {
		return ""
	}
	return rest[q1+1 : q1+1+q2]
}
