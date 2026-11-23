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
  stageproof check    --repo <url> --chart <name> --from <version> --to <version> [flags]
  stageproof check    --argocd-file <app.yaml> --current-git <git-ref> [flags]
  stageproof check    --flux-file <helmrelease.yaml> --current-git <git-ref> [flags]
  stageproof check    --before <dir|file> --after <dir|file> [flags]
  stageproof check-pr --current-git <base-sha> [--new-git HEAD] [flags]
  stageproof fetch-apiserver --k8s <minor> --out <dir>
  stageproof explain <rule-id>
  stageproof comment --file <comment.md> [--provider auto|github|gitlab]
  stageproof mcp     [--root <dir>]
  stageproof facts export --kubeconfig <file> --context <name> --out <file>

Results:
  PROVEN       exit 0   file checks passed and the upgrade was rehearsed
  PASSED       exit 0   file checks passed; the upgrade was not rehearsed
  BLOCKED      exit 1   the upgrade has a problem the report names
  NOT CHECKED  exit 2   the check did not finish, or --require-rehearsal was set and no rehearsal ran

Chart, Argo CD & Values:
  --current-chart <dir>    Currently deployed Helm chart directory (before upgrade)
  --new-chart <dir>        New candidate Helm chart directory to verify (after upgrade)
  --repo <url>             Helm http(s) repository or oci:// registry. Requires --chart, --from, and --to.
                           Those three are the chart name and the two versions. The charts are downloaded
                           and removed when the command exits. Without --repo, --chart, --from, and --to
                           stay aliases for local chart directories.
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
  --cluster-facts <file>   JSON from stageproof facts export. Sets the Kubernetes version when --current-k8s is not set, preloads those custom resource definitions, and passes stored versions and node architectures into the check.
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

Comment:
  stageproof comment reads that Markdown file and posts it, or updates the comment that already contains the marker.
  --file <file>            Markdown from --comment-file
  --provider <name>        auto (default), github, or gitlab. auto uses GITHUB_ACTIONS or GITLAB_CI.
  --pr <number>            GitHub pull request. Default is the current Actions event.
  --mr <iid>               GitLab merge request. Default is CI_MERGE_REQUEST_IID.
  --repo <owner/name>      GitHub repository, or a GitLab project id or group/project path.
  --marker <text>          Default is <!-- stageproof-comment -->. A later run updates that comment.
  --best-effort            Exit 0 when the server returns HTTP 403. The default exit is 2.

MCP:
  stageproof mcp speaks MCP on stdin and stdout. It does not open a port. Logs go to stderr.
  --root <dir>             Files the tools may read. Default is the current directory.
                           Absolute paths and paths outside this directory are rejected.
                           Live rehearsal and report files are off.

Examples:
  # 1. Compare current chart vs new chart with your prod values + check K8s 1.33 & 1.34
  stageproof check --current-chart ./charts/argo-v5 --new-chart ./charts/argo-v10 -f values-prod.yaml --current-k8s 1.33 --next-k8s 1.34

  # 2. Check a pull request from its git diff. Exit 0 is green. Exit 1 and 2 fail CI.
  stageproof check-pr --current-git "$CI_MERGE_REQUEST_DIFF_BASE_SHA" --current-k8s 1.35 --next-k8s 1.36 --comment-file comment.md

  # 3. Print what one rule means. Exit 2 when the id is not a rule.
  stageproof explain helm.values.silently_dropped

  # 4. Compare two directories of rendered manifests. Helm is not called.
  stageproof check --before ./before --after ./after --current-k8s 1.35

  # 5. Read a cluster you name, then check with that file. The export uses get and list only.
  stageproof facts export --kubeconfig ~/.kube/prod --context prod --out facts.json
  stageproof check --before rendered/main --after rendered/pr --cluster-facts facts.json

  # 6. Compare two versions from a Helm repository. The charts are downloaded and removed.
  stageproof check --repo https://charts.jetstack.io --chart cert-manager --from v1.21.2 --to v1.21.3 --current-k8s 1.35
`)
}
