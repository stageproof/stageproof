package main

import (
	"fmt"
	"os"
)

func printUsage() {
	fmt.Fprint(os.Stderr, `stageproof - deterministic Helm and Kubernetes upgrade checks

Usage:
  stageproof init     [--dir .] [--out .stageproof.yaml]
  stageproof check    --current-chart <dir> --new-chart <dir> [flags]
  stageproof check    --argocd-file <app.yaml> --current-git <git-ref> [flags]
  stageproof check    --flux-file <helmrelease.yaml> --current-git <git-ref> [flags]
  stageproof check    --before <dir|file> --after <dir|file> [flags]
  stageproof check-pr --current-git <base-sha> [--new-git HEAD] [flags]
  stageproof fetch-apiserver --k8s <minor> --out <dir>
  stageproof explain <rule-id>

Results:
  PROVEN       exit 0   file checks passed and the upgrade was rehearsed
  PASSED       exit 0   file checks passed; the upgrade was not rehearsed
  BLOCKED      exit 1   the upgrade has a problem the report names
  NOT CHECKED  exit 2   the check did not finish, or --require-rehearsal was set and no rehearsal ran

Chart, Argo CD & Values:
  --current-chart <dir>    Currently deployed Helm chart directory (before upgrade)
  --new-chart <dir>        New candidate Helm chart directory to verify (after upgrade)
  --argocd-file <file>     Argo CD Application YAML / .yaml.tmpl. A remote chart is pulled. A Git path in this checkout is read locally.
  --current-argocd-file <file>
                           Previously deployed Argo CD Application, compared with --argocd-file
  --flux-file <file>       Flux HelmRelease. A HelmRepository or OCI chart is pulled. A GitRepository in this checkout is read locally.
  --current-flux-file <file>
                           Previously deployed Flux HelmRelease, compared with --flux-file
  --current-git <ref>      Extract current chart / Argo CD file from git ref (e.g. origin/main)
  --before <dir|file>      Rendered manifests before the change (.yaml, .yml, or a directory)
  --after <dir|file>       Rendered manifests after the change. Not a Helm chart, and not combined with --live.
  -f, --values <file>      Your values.yaml file(s) (repeatable)
  --base-values <file>     Optional separate values.yaml for --current-chart (defaults to -f)
  --set <k=v>              Explicit Helm --set override(s) (repeatable)
  --values-proof <mode>    default, or full. default renders keys the template reader drops or cannot decide. full renders every user key and every changed chart default.
  --offline                Do not contact container registries. Does not change --api-server.
  --helm-compare <path>    Render with this helm binary as well. A difference is a warning. A binary that cannot render the chart blocks.

Kubernetes & GitOps Environment:
  --current-k8s <ver>      Current cluster Kubernetes version (default "1.33")
  --next-k8s <ver>         Next Kubernetes version you plan to upgrade to (e.g. "1.34")
  --api-server <mode>      auto (default), off, or require. auto asks a local API server of that minor and uses the built-in table if the binaries are not available. require exits 2 instead.
  --api-server-dir <dir>   kube-apiserver, etcd, and kubectl already on disk. Nothing is downloaded.
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
  --format <fmt>           text | markdown | json | sarif (default "text")
  --out <file>             Save JSON evidence report to file
  --comment-file <file>    Also write the Markdown merge-request comment (job log stays colored text)

Examples:
  # 1. Compare current chart vs new chart with your prod values + check K8s 1.33 & 1.34
  stageproof check --current-chart ./charts/argo-v5 --new-chart ./charts/argo-v10 -f values-prod.yaml --current-k8s 1.33 --next-k8s 1.34

  # 2. Check a pull request from its git diff. Exit 0 is green. Exit 1 and 2 fail CI.
  stageproof check-pr --current-git "$CI_MERGE_REQUEST_DIFF_BASE_SHA" --current-k8s 1.35 --next-k8s 1.36 --comment-file comment.md

  # 3. Print what one rule means. Exit 2 when the id is not a rule.
  stageproof explain helm.values.silently_dropped

  # 4. Compare two directories of rendered manifests. Helm is not called.
  stageproof check --before ./before --after ./after --current-k8s 1.35
`)
}
