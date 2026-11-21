package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stageproof/stageproof/internal/argocd"
	"github.com/stageproof/stageproof/internal/check"
	"github.com/stageproof/stageproof/internal/config"
	"github.com/stageproof/stageproof/internal/flux"
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/rehearse"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "init":
		os.Exit(runInit(os.Args[2:]))
	case "check":
		os.Exit(runCheckCtx(ctx, os.Args[2:]))
	case "check-pr", "check-mr":
		os.Exit(runCheckPRCtx(ctx, os.Args[2:]))
	case "fetch-apiserver":
		os.Exit(runFetchAPIServer(ctx, os.Args[2:]))
	case "explain":
		os.Exit(runExplain(os.Args[2:]))
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
	return runCheckCtx(context.Background(), args)
}

func runCheckCtx(ctx context.Context, args []string) int {
	return runCheckCollect(ctx, args, nil)
}

// runCheckCollect runs one check. When collected is not nil the report is also
// appended to it, so that check-pr can combine the reports of several charts.
func runCheckCollect(ctx context.Context, args []string, collected *[]*verdict.Report) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.Usage = printUsage

	// Primary matched flags: --current-* / --new-* + --argocd-file + --base-values
	currentChart := fs.String("current-chart", "", "Currently deployed Helm chart directory")
	newChart := fs.String("new-chart", "", "New candidate Helm chart directory")
	argoFile := fs.String("argocd-file", "", "Argo CD Application YAML or .yaml.tmpl file")
	currentArgo := fs.String("current-argocd-file", "", "Previously deployed Argo CD Application file, compared with --argocd-file")
	fluxFile := fs.String("flux-file", "", "Flux HelmRelease YAML file")
	currentFlux := fs.String("current-flux-file", "", "Previously deployed Flux HelmRelease, compared with --flux-file")
	currentGit := fs.String("current-git", "", "Git ref to extract --current-chart, --argocd-file, or --flux-file from")
	before := fs.String("before", "", "Rendered manifests before the change, a .yaml/.yml file or a directory")
	after := fs.String("after", "", "Rendered manifests after the change, a .yaml/.yml file or a directory")

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
	apiServer := fs.String("api-server", "auto", "API server proof: auto, off, or require")
	apiServerDir := fs.String("api-server-dir", "", "Directory of kube-apiserver, etcd, and kubectl already on disk")
	valuesProof := fs.String("values-proof", "default", "Values proof: default, or full to render every user key")
	offline := fs.Bool("offline", false, "Do not contact container registries. Does not change --api-server.")
	helmCompare := fs.String("helm-compare", "", "Path to a second helm binary. Renders the chart with both.")
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

	beforePath := strings.TrimSpace(*before)
	afterPath := strings.TrimSpace(*after)
	manifestMode := beforePath != "" || afterPath != ""
	if manifestMode {
		if beforePath == "" || afterPath == "" {
			fmt.Fprintln(os.Stderr, "stageproof check: --before and --after are both required")
			return 2
		}
		if headDir != "" || baseDir != "" || *argoFile != "" || *currentArgo != "" || *fluxFile != "" || *currentFlux != "" || gitRef != "" {
			fmt.Fprintln(os.Stderr, "stageproof check: --before and --after cannot be combined with a chart, an Argo CD Application, or a Flux HelmRelease")
			return 2
		}
		if len(valuesFiles) > 0 || len(baseValuesFiles) > 0 || len(setValues) > 0 {
			fmt.Fprintln(os.Stderr, "stageproof check: values files and --set are for Helm charts. Manifest mode reads the YAML you already rendered.")
			return 2
		}
		if effLive {
			fmt.Fprintln(os.Stderr, "stageproof check: --before and --after cannot be combined with a live rehearsal. Manifest mode does not install a chart.")
			return 2
		}
	}
	if !manifestMode && headDir == "" && *argoFile == "" && *fluxFile == "" {
		fmt.Fprintln(os.Stderr, "error: --new-chart <dir>, --argocd-file <file>, or --flux-file <file> is required")
		fmt.Fprintln(os.Stderr, "Run `stageproof check --help` for examples.")
		return 2
	}

	if *argoFile != "" && *fluxFile != "" {
		fmt.Fprintln(os.Stderr, "stageproof check: --argocd-file and --flux-file cannot be combined")
		return 2
	}

	tStart := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	switch *apiServer {
	case "auto", "off", "require":
	default:
		fmt.Fprintf(os.Stderr, "error: --api-server must be auto, off, or require\n")
		return 2
	}
	switch *valuesProof {
	case "default", "full":
	default:
		fmt.Fprintf(os.Stderr, "error: --values-proof must be default or full\n")
		return 2
	}

	if *helmCompare != "" && manifestMode {
		fmt.Fprintf(os.Stderr, "error: --helm-compare renders a chart and cannot be combined with --before or --after\n")
		return 2
	}

	if manifestMode {
		rep := check.Run(ctx, check.Input{
			Adapter:          "manifests",
			ManifestMode:     true,
			ManifestBefore:   beforePath,
			ManifestAfter:    afterPath,
			BaseRef:          beforePath,
			HeadRef:          afterPath,
			KubeVersion:      effK8s,
			NextKubernetes:   effNextK8s,
			APIServer:        *apiServer,
			APIServerDir:     *apiServerDir,
			ServerSideApply:  effSSA,
			RequireRehearsal: effRequire,
			Offline:          *offline,
			Started:          tStart,
		})
		emit(rep)
		return rep.ExitCode
	}

	resolvedBaseDir := baseDir
	effectiveValues := []string(valuesFiles)
	baseVals := []string(baseValuesFiles)
	baseLabel := "(single-chart check)"
	headLabel := headDir
	// Values written inside an Argo CD Application are checked from a temporary
	// file. Reports name where the values came from, not that file.
	valuesLabel := map[string]string{}
	var placeholderTokens []string
	var headRelease, baseRelease, headNamespace, baseNamespace string
	var headSet, baseSet, headSetStr, baseSetStr, headSetFile, baseSetFile []string
	var argoSkip *bool
	var fluxPolicy string
	var valueGaps []string
	headSkipCRDs := false
	baseSkipCRDs := false
	argoBase := false

	// Argo CD Application. A remote chart is pulled. A Git path in this
	// checkout is read from the revision being compared. Other Git
	// repositories are not cloned.
	if *argoFile != "" {
		placeholders := templatePlaceholders()
		newSpec, used, aErr := argocd.ParseApplicationFile(*argoFile, placeholders)
		if rep, ok := argoStop(aErr, *argoFile); ok {
			emit(rep)
			return verdict.NotChecked.ExitCode()
		}
		if aErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing --argocd-file %s: %v\n", *argoFile, aErr)
			return 2
		}
		placeholderTokens = append(placeholderTokens, used...)
		headMat, mErr := argocd.Materialize(ctx, newSpec, argocd.MaterializeOptions{})
		if rep, ok := argoStop(mErr, *argoFile); ok {
			emit(rep)
			return verdict.NotChecked.ExitCode()
		}
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "error reading --argocd-file %s: %v\n", *argoFile, mErr)
			return 2
		}
		defer headMat.Cleanup()
		headDir = headMat.ChartDir
		headLabel = argoRefLabel(newSpec, *argoFile)
		if newSpec.ServerSideApply {
			effSSA = true
		}
		effectiveValues = append(effectiveValues, headMat.ValueFiles...)
		for path, label := range headMat.Labels {
			valuesLabel[path] = label
		}
		headSet, headSetStr, headSetFile = headMat.SetValues, headMat.SetStrings, headMat.SetFiles
		headRelease, headNamespace = headMat.ReleaseName, headMat.Namespace
		argoSkip = newSpec.SkipCrds
		headSkipCRDs = newSpec.SkipCrds != nil && *newSpec.SkipCrds

		var oldSpec *argocd.AppSpec
		oldLabel := ""
		if *currentArgo != "" {
			parsed, usedOld, oErr := argocd.ParseApplicationFile(*currentArgo, placeholders)
			if rep, ok := argoStop(oErr, *currentArgo); ok {
				emit(rep)
				return verdict.NotChecked.ExitCode()
			}
			if oErr != nil {
				fmt.Fprintf(os.Stderr, "error parsing --current-argocd-file %s: %v\n", *currentArgo, oErr)
				return 2
			}
			oldSpec = parsed
			oldLabel = *currentArgo
			placeholderTokens = append(placeholderTokens, usedOld...)
		} else if gitRef != "" {
			oldBytes, gErr := readFileFromGitRef(gitRef, *argoFile)
			if gErr == nil {
				parsed, usedOld, oErr := argocd.ParseApplicationBytes(oldBytes, gitRef+":"+*argoFile, placeholders)
				if rep, ok := argoStop(oErr, gitRef+":"+*argoFile); ok {
					emit(rep)
					return verdict.NotChecked.ExitCode()
				}
				if oErr == nil {
					oldSpec = parsed
					oldLabel = gitRef
					placeholderTokens = append(placeholderTokens, usedOld...)
				}
			}
		}
		if oldSpec != nil {
			baseMat, bErr := argocd.Materialize(ctx, oldSpec, argocd.MaterializeOptions{GitRef: gitRefFor(oldLabel, *currentArgo)})
			if rep, ok := argoStop(bErr, oldLabel); ok {
				emit(rep)
				return verdict.NotChecked.ExitCode()
			}
			if bErr != nil {
				fmt.Fprintf(os.Stderr, "error reading the current Application %s: %v\n", oldLabel, bErr)
				return 2
			}
			defer baseMat.Cleanup()
			resolvedBaseDir = baseMat.ChartDir
			baseLabel = argoRefLabel(oldSpec, oldLabel)
			argoBase = true
			if len(baseVals) == 0 {
				baseVals = append([]string{}, baseMat.ValueFiles...)
			}
			for path, label := range baseMat.Labels {
				if _, exists := valuesLabel[path]; !exists {
					valuesLabel[path] = label
				}
			}
			baseSet, baseSetStr, baseSetFile = baseMat.SetValues, baseMat.SetStrings, baseMat.SetFiles
			baseRelease, baseNamespace = baseMat.ReleaseName, baseMat.Namespace
			baseSkipCRDs = oldSpec.SkipCrds != nil && *oldSpec.SkipCrds
		}
	} else if *fluxFile != "" {
		placeholders := templatePlaceholders()
		newRel, _, fErr := flux.ParseFile(*fluxFile, placeholders)
		if rep, ok := fluxStop(fErr, *fluxFile); ok {
			emit(rep)
			return verdict.NotChecked.ExitCode()
		}
		if fErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing --flux-file %s: %v\n", *fluxFile, fErr)
			return 2
		}
		headMat, mErr := flux.Materialize(ctx, newRel, flux.MaterializeOptions{})
		if rep, ok := fluxStop(mErr, *fluxFile); ok {
			emit(rep)
			return verdict.NotChecked.ExitCode()
		}
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "error reading --flux-file %s: %v\n", *fluxFile, mErr)
			return 2
		}
		defer headMat.Cleanup()
		headDir = headMat.ChartDir
		headLabel = fluxRefLabel(headMat, *fluxFile)
		effectiveValues = append(effectiveValues, headMat.ValueFiles...)
		for path, label := range headMat.Labels {
			valuesLabel[path] = label
		}
		headRelease, headNamespace = headMat.ReleaseName, headMat.Namespace
		fluxPolicy = newRel.UpgradeCRDs
		valueGaps = append(valueGaps, headMat.Gaps...)
		placeholderTokens = append(placeholderTokens, headMat.Used...)

		var oldRel *flux.Release
		oldLabel := ""
		if *currentFlux != "" {
			parsed, _, oErr := flux.ParseFile(*currentFlux, placeholders)
			if rep, ok := fluxStop(oErr, *currentFlux); ok {
				emit(rep)
				return verdict.NotChecked.ExitCode()
			}
			if oErr != nil {
				fmt.Fprintf(os.Stderr, "error parsing --current-flux-file %s: %v\n", *currentFlux, oErr)
				return 2
			}
			oldRel = parsed
			oldLabel = *currentFlux
		} else if gitRef != "" {
			oldBytes, gErr := readFileFromGitRef(gitRef, *fluxFile)
			if gErr == nil {
				parsed, _, oErr := flux.ParseBytes(oldBytes, gitRef+":"+*fluxFile, placeholders)
				if rep, ok := fluxStop(oErr, gitRef+":"+*fluxFile); ok {
					emit(rep)
					return verdict.NotChecked.ExitCode()
				}
				if oErr == nil {
					oldRel = parsed
					oldLabel = gitRef
				}
			}
		}
		if oldRel != nil {
			baseMat, bErr := flux.Materialize(ctx, oldRel, flux.MaterializeOptions{GitRef: gitRefFor(oldLabel, *currentFlux)})
			if rep, ok := fluxStop(bErr, oldLabel); ok {
				emit(rep)
				return verdict.NotChecked.ExitCode()
			}
			if bErr != nil {
				fmt.Fprintf(os.Stderr, "error reading the current HelmRelease %s: %v\n", oldLabel, bErr)
				return 2
			}
			defer baseMat.Cleanup()
			resolvedBaseDir = baseMat.ChartDir
			baseLabel = fluxRefLabel(baseMat, oldLabel)
			argoBase = true
			prefix := []string(baseValuesFiles)
			if len(prefix) == 0 {
				prefix = []string(valuesFiles)
			}
			baseVals = append(append([]string{}, prefix...), baseMat.ValueFiles...)
			for path, label := range baseMat.Labels {
				if _, exists := valuesLabel[path]; !exists {
					valuesLabel[path] = label
				}
			}
			baseRelease, baseNamespace = baseMat.ReleaseName, baseMat.Namespace
			valueGaps = append(valueGaps, baseMat.Gaps...)
			placeholderTokens = append(placeholderTokens, baseMat.Used...)
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

	if len(baseVals) == 0 && !argoBase {
		baseVals = effectiveValues
	}
	if yml, yErr := helm.AssignmentsYAML(setValues); yErr == nil && strings.TrimSpace(yml) != "" {
		dir, dErr := os.MkdirTemp("", "stageproof-set-*")
		if dErr == nil {
			defer func() { _ = os.RemoveAll(dir) }()
			setPath := filepath.Join(dir, "parameters.yaml")
			if wErr := os.WriteFile(setPath, []byte(yml), 0o644); wErr == nil {
				effectiveValues = append(effectiveValues, setPath)
				valuesLabel[setPath] = "helm.parameters"
			}
		}
	}

	adapterName := "helm"
	if *argoFile != "" {
		adapterName = "argocd"
	}
	if *fluxFile != "" {
		adapterName = "flux"
	}
	rep := check.Run(ctx, check.Input{
		Adapter:           adapterName,
		BaseDir:           resolvedBaseDir,
		HeadDir:           headDir,
		BaseRef:           baseLabel,
		HeadRef:           headLabel,
		BaseValues:        baseVals,
		HeadValues:        effectiveValues,
		ValueLabels:       valuesLabel,
		SetValues:         setValues,
		BaseSet:           baseSet,
		HeadSet:           headSet,
		BaseSetString:     baseSetStr,
		HeadSetString:     headSetStr,
		BaseSetFile:       baseSetFile,
		HeadSetFile:       headSetFile,
		BaseRelease:       baseRelease,
		HeadRelease:       headRelease,
		BaseNamespace:     baseNamespace,
		HeadNamespace:     headNamespace,
		BaseSkipCRDs:      baseSkipCRDs,
		HeadSkipCRDs:      headSkipCRDs,
		ArgoSkipCrds:      argoSkip,
		FluxCRDPolicy:     fluxPolicy,
		ValueGaps:         valueGaps,
		KubeVersion:       effK8s,
		NextKubernetes:    effNextK8s,
		APIServer:         *apiServer,
		APIServerDir:      *apiServerDir,
		ServerSideApply:   effSSA,
		RequireRehearsal:  effRequire,
		Live:              effLive,
		ClusterName:       *clusterName,
		KeepCluster:       *keepCluster,
		Timeout:           *timeout,
		HelmTest:          *runHelmTest,
		CRDFiles:          crdFiles,
		PlaceholderTokens: placeholderTokens,
		ValuesFull:        *valuesProof == "full",
		Offline:           *offline,
		HelmCompare:       *helmCompare,
		Started:           tStart,
	})
	emit(rep)
	return rep.ExitCode
}

func fluxStop(err error, label string) (*verdict.Report, bool) {
	if err == nil {
		return nil, false
	}
	if tokens := flux.UnresolvedTokens(err); len(tokens) > 0 {
		rep := unresolvedPlaceholderReport(label, tokens)
		rep.Adapter = "flux"
		return rep, true
	}
	var skipped *flux.NotCheckedError
	if !errors.As(err, &skipped) {
		return nil, false
	}
	return &verdict.Report{
		Verdict:   verdict.NotChecked,
		ExitCode:  verdict.NotChecked.ExitCode(),
		Adapter:   "flux",
		BaseRef:   label,
		HeadRef:   label,
		Checked:   verdict.CheckedFiles,
		Timestamp: time.Now().UTC(),
		Checks: []verdict.CheckResult{{
			Name:    "Flux source",
			Status:  "fail",
			Summary: skipped.Error(),
		}},
	}, true
}

func fluxRefLabel(mat *flux.Materialized, source string) string {
	if mat == nil || mat.RefLabel == "" {
		return source
	}
	if source == "" || strings.HasPrefix(source, os.TempDir()) {
		return mat.RefLabel
	}
	return mat.RefLabel + " (" + source + ")"
}

func argoStop(err error, label string) (*verdict.Report, bool) {
	if err == nil {
		return nil, false
	}
	if tokens := argocd.UnresolvedTokens(err); len(tokens) > 0 {
		return unresolvedPlaceholderReport(label, tokens), true
	}
	var skipped *argocd.NotCheckedError
	if !errors.As(err, &skipped) {
		return nil, false
	}
	return &verdict.Report{
		Verdict:   verdict.NotChecked,
		ExitCode:  verdict.NotChecked.ExitCode(),
		Adapter:   "argocd",
		BaseRef:   label,
		HeadRef:   label,
		Checked:   verdict.CheckedFiles,
		Timestamp: time.Now().UTC(),
		Checks: []verdict.CheckResult{{
			Name:    "Argo CD source",
			Status:  "fail",
			Summary: skipped.Error(),
		}},
	}, true
}

func argoRefLabel(spec *argocd.AppSpec, source string) string {
	if spec.LocalPath != "" {
		if source == "" || strings.HasPrefix(source, os.TempDir()) {
			return spec.LocalPath
		}
		return spec.LocalPath + " (" + source + ")"
	}
	return chartRefLabel(spec.Chart, spec.TargetRevision, source)
}

// gitRefFor is the revision to read a Git-path chart from. A separate
// --current-argocd-file is already the old spec, so its chart path is the
// working tree. --current-git is the old revision of this checkout.
func gitRefFor(oldLabel, currentArgo string) string {
	if currentArgo != "" {
		return ""
	}
	return oldLabel
}
