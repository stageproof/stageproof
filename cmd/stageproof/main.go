package main

import (
	"context"
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
	currentGit := fs.String("current-git", "", "Git ref to extract --current-chart / --argocd-file from")
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
		if headDir != "" || baseDir != "" || *argoFile != "" || *currentArgo != "" || gitRef != "" {
			fmt.Fprintln(os.Stderr, "stageproof check: --before and --after cannot be combined with a chart or an Argo CD Application")
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
	if !manifestMode && headDir == "" && *argoFile == "" {
		fmt.Fprintln(os.Stderr, "error: --new-chart <dir> or --argocd-file <file> is required")
		fmt.Fprintln(os.Stderr, "Run `stageproof check --help` for examples.")
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

	// Argo CD Application / .yaml.tmpl mode: auto-pull remote Helm chart(s), extract inline values & syncOptions!
	if *argoFile != "" {
		newSpec, used, aErr := argocd.ParseApplicationFile(*argoFile, templatePlaceholders())
		if tokens := argocd.UnresolvedTokens(aErr); len(tokens) > 0 {
			emit(unresolvedPlaceholderReport(*argoFile, tokens))
			return verdict.NotChecked.ExitCode()
		}
		if aErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing --argocd-file %s: %v\n", *argoFile, aErr)
			return 2
		}
		placeholderTokens = append(placeholderTokens, used...)
		tmpPull, mErr := os.MkdirTemp("", "stageproof-argopull-*")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "tempdir error: %v\n", mErr)
			return 2
		}
		defer func() { _ = os.RemoveAll(tmpPull) }()

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
			parsed, usedOld, oErr := argocd.ParseApplicationFile(*currentArgo, templatePlaceholders())
			if tokens := argocd.UnresolvedTokens(oErr); len(tokens) > 0 {
				emit(unresolvedPlaceholderReport(*currentArgo, tokens))
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
				parsed, usedOld, oErr := argocd.ParseApplicationBytes(oldBytes, gitRef+":"+*argoFile, templatePlaceholders())
				if tokens := argocd.UnresolvedTokens(oErr); len(tokens) > 0 {
					emit(unresolvedPlaceholderReport(gitRef+":"+*argoFile, tokens))
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
		Started:           tStart,
	})
	emit(rep)
	return rep.ExitCode
}
