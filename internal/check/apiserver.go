package check

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stageproof/stageproof/internal/apiserver"
	"github.com/stageproof/stageproof/internal/facts"
	"github.com/stageproof/stageproof/internal/helm"
	"github.com/stageproof/stageproof/internal/k8sschema"
	"github.com/stageproof/stageproof/internal/knowledge"
	"github.com/stageproof/stageproof/internal/verdict"
)

// prepareAPIs populates st.ServedAPIs for helm template before Charts renders.
// When --api-server is auto or require, it starts the local API server once
// and reads its discovery; ServerProof reuses that running server and stops it.
// When --api-server is off or binaries are unavailable, it uses the stored
// discovery list for that Kubernetes minor from internal/knowledge.
func prepareAPIs(ctx context.Context, in Input, st *State) {
	mode := in.APIServer
	if mode == "auto" || mode == "require" {
		st.apiServerPrepared = true
		srv, err := apiserver.Start(ctx, in.KubeVersion, in.APIServerDir)
		if err != nil {
			st.apiServerStartErr = err
			st.ServedAPIs = knowledge.ServedAPIs(in.KubeVersion)
			return
		}
		st.apiServer = srv
		if apis, derr := srv.ServedAPIs(); derr == nil && len(apis) > 0 {
			st.ServedAPIs = apis
			return
		}
	}
	st.ServedAPIs = knowledge.ServedAPIs(in.KubeVersion)
}

// ServerProof asks a local API server of the requested minor whether the
// upgrade is accepted. --api-server off leaves the built-in table in place.
func ServerProof(ctx context.Context, in Input, st *State) {
	defer st.closeAPIServer()
	mode := in.APIServer
	if mode == "" || mode == "off" {
		return
	}
	if mode != "auto" && mode != "require" {
		st.Report.Skipped = append(st.Report.Skipped, verdict.SkipNotice{
			Code:        "api_server_mode",
			Reason:      "Unknown --api-server value " + mode + ".",
			Remediation: "Use auto, off, or require.",
		})
		st.stop(false, true)
		return
	}

	var srv *apiserver.Server
	var err error
	if st.apiServerPrepared {
		srv = st.apiServer
		err = st.apiServerStartErr
	} else {
		srv, err = apiserver.Start(ctx, in.KubeVersion, in.APIServerDir)
		if err == nil {
			st.apiServer = srv
		}
	}
	if err != nil {
		if mode == "require" {
			st.Report.Skipped = append(st.Report.Skipped, verdict.SkipNotice{
				Code:        "api_server_required",
				Reason:      "API server binaries for Kubernetes " + in.KubeVersion + " are not available.",
				Remediation: "Run `stageproof fetch-apiserver --k8s " + in.KubeVersion + " --out <dir>` and pass `--api-server-dir`.",
			})
			st.stop(false, true)
			return
		}
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "API server",
			Status:  "skip",
			Summary: "Kubernetes " + in.KubeVersion + " binaries were not available. The built-in API table was used.",
		})
		return
	}

	preload, preloadErr := clusterPreload(in.Facts)
	if preloadErr != nil {
		st.Report.Skipped = append(st.Report.Skipped, verdict.SkipNotice{
			Code:        "cluster_facts",
			Reason:      "Cluster facts could not be read as custom resource definitions.",
			Remediation: "Run `stageproof facts export` again and pass that file to `--cluster-facts`.",
		})
		st.stop(false, true)
		return
	}
	strict := strictUnknown(in.ServerSideApply, helm.Major(ctx), in.ManifestMode, in.BaseDir != "")
	opt := apiserver.ProofOptions{StrictUnknown: strict, Preload: preload}
	res, err := srv.Prove(ctx, manifests(st.BaseObjs), manifests(st.HeadObjs), crdManifests(in.BaseDir), crdManifests(in.HeadDir), opt)
	if err != nil {
		if mode == "require" {
			st.Report.Skipped = append(st.Report.Skipped, verdict.SkipNotice{
				Code:        "api_server_required",
				Reason:      "The API server for Kubernetes " + in.KubeVersion + " did not finish.",
				Remediation: "Re-run the check. A repeated failure means the binaries for that minor did not start.",
			})
			st.stop(false, true)
			return
		}
		st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
			Name:    "API server",
			Status:  "skip",
			Summary: "The API server did not finish. The built-in API table was used.",
		})
		return
	}
	if !in.ManifestMode {
		res.Findings = downgradeGuardedRemovedAPIs(in.HeadDir, st.HeadObjs, res.Findings)
	}
	st.APIServerRan = true
	st.APIServerVersion = srv.Version()
	st.StoredBefore = append(st.StoredBefore, res.StoredBefore...)
	st.StoredAfter = append(st.StoredAfter, res.StoredAfter...)
	st.Report.Findings = append(st.Report.Findings, res.Findings...)
	st.Report.Checked = checkedWithAPIServer(in, srv.Version())
	blockers := 0
	for _, f := range res.Findings {
		if f.Severity == verdict.SeverityBlocker {
			blockers++
		}
	}
	status := "pass"
	summary := fmt.Sprintf("Kubernetes %s accepted the upgrade", srv.Version())
	if blockers > 0 {
		status = "fail"
		summary = counted(blockers, "rejection", "rejections")
	}
	st.Report.Checks = append(st.Report.Checks, verdict.CheckResult{
		Name:    "API server " + srv.Version(),
		Status:  status,
		Summary: summary,
	})

	if in.NextKubernetes == "" {
		return
	}
	// The directory flag is the binaries for the current minor. The next minor
	// is a different archive, taken from the cache when it can be downloaded.
	next, nerr := apiserver.Start(ctx, in.NextKubernetes, "")
	if nerr != nil {
		// The current proof stands. The table still covers the next minor.
		return
	}
	defer func() { _ = next.Stop() }()
	nextRes, nerr := next.Prove(ctx, nil, manifests(st.HeadObjs), nil, crdManifests(in.HeadDir), apiserver.ProofOptions{
		StrictUnknown: strict,
		CreatesOnly:   true,
		Warnings:      true,
		Preload:       preload,
	})
	if nerr != nil {
		return
	}
	st.NextAPIServerRan = true
	st.Report.Findings = append(st.Report.Findings, nextRes.Findings...)
}

func checkedWithAPIServer(in Input, version string) string {
	if in.ManifestMode {
		return verdict.CheckedManifests + ", API server " + version
	}
	return "files, API server " + version
}

func clusterPreload(doc *facts.Document) ([]apiserver.Manifest, error) {
	if doc == nil {
		return nil, nil
	}
	objs, err := doc.CRDObjects()
	if err != nil {
		return nil, err
	}
	out := make([]apiserver.Manifest, 0, len(objs))
	for _, obj := range objs {
		out = append(out, apiserver.Manifest{Raw: obj})
	}
	return out, nil
}

func manifests(objs []k8sschema.ManifestObject) []apiserver.Manifest {
	out := make([]apiserver.Manifest, 0, len(objs))
	for _, obj := range objs {
		if len(obj.Object) == 0 {
			continue
		}
		out = append(out, apiserver.Manifest{Raw: obj.Object})
	}
	return out
}

func crdManifests(dir string) []apiserver.Manifest {
	if dir == "" {
		return nil
	}
	var out []apiserver.Manifest
	for _, path := range crdFiles(dir) {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		objs, err := k8sschema.ParseManifestStream(string(raw))
		if err != nil {
			continue
		}
		out = append(out, manifests(objs)...)
	}
	return out
}

// crdFiles walks chart/crds and chart/charts/*/crds, including nested directories.
func crdFiles(chartDir string) []string {
	roots := []string{filepath.Join(chartDir, "crds")}
	entries, err := os.ReadDir(filepath.Join(chartDir, "charts"))
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(chartDir, "charts", entry.Name(), "crds"))
			}
		}
	}
	var files []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml", ".json":
				files = append(files, path)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}
