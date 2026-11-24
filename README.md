# Stageproof

Stageproof checks a Kubernetes upgrade before you merge it: Helm charts, Argo CD Applications, Flux HelmReleases, and rendered YAML manifests.

You give it what you run today and what the pull request wants. It uses your values and the Kubernetes version your cluster already runs. It prints one word and an exit code. The pull request is only read. The cluster you deploy to is never contacted.

The released binary is `v0.3.1`. It checks Helm charts, Argo CD Applications, Flux HelmReleases, and rendered manifests. It includes local API server proof, render-based values proof, impact and CRD checks, image tag checks, Helm 4 comparison, cluster facts, `explain`, SARIF, ignores, policy, and live `smoke` commands.

Chart-default upgrade reports for 300 public Helm charts are at the [Stageproof index](https://stageproof.github.io/index/). The index has more than 20,000 upgrade pairs. Each report uses that chart's default values, not the values a cluster runs.

## Check a pull request

```bash
stageproof check-pr \
  --current-git "$BASE" \
  --current-k8s 1.35 \
  --next-k8s 1.36 \
  --comment-file stageproof-comment.md \
  --out stageproof-report.json
```

`$BASE` is the branch the pull request starts from (`origin/main`, or the base SHA your CI already exports). The commit being checked is `HEAD`.

This reads the files. It usually finishes in a few seconds. The report says `NOT TESTED LIVE`, which means the upgrade was not started.

```text
stageproof  PASSED  exit 0
compare  current/metrics-server -> new/metrics-server
k8s  1.33  ·  checked: files

  ✓ PASS  helm template            rendered 9 Kubernetes objects cleanly
  ✓ PASS  values.yaml AST          no custom -f overlay passed (default chart values verified)
  ✓ PASS  k8s 1.33 compatibility   0 removed APIs or selector breaks on K8s 1.33

verdict  PASSED (exit 0)  · 87ms · 0 AI calls
```

That sample is metrics-server 3.12.2 to 3.13.0. `values.yaml AST` is the check that your values keys are still read. `checked: files` means no API server was started. The default check starts one, and the line then names the patch, for example `checked: files, API server 1.35.0`. The result is still `PASSED` until you pass `--live`.

Exit 0 is `PASSED` or `PROVEN`. Exit 1 and exit 2 fail the job. GitHub Actions and GitLab CI examples are in the [documentation](https://stageproof.github.io/stageproof/docs.html).

### GitHub Actions

```yaml
      - uses: stageproof/action@v1
        with:
          current-k8s: "1.35"
          api-server: auto
          comment: "true"
          live: "false"
```

The job needs `permissions: pull-requests: write`. The action checks out the pull request, downloads Stageproof `v0.3.1`, checks `checksums.txt`, and adds the report to the job summary. It posts one comment, or updates the comment that already contains `<!-- stageproof-comment -->`. A fork pull request often has a read-only token. The comment step treats that as success and still returns the check exit code. Do not run this action from `pull_request_target`. `live: "true"` starts a temporary kind cluster, and the runner has to be able to start containers.

### The four words

| Word | Exit | What happened |
|---|---|---|
| `PASSED` | 0 | The files are fine. No temporary cluster was used, or the try stopped because of something outside the chart. The job stays green. The report says which. |
| `PROVEN` | 0 | The files are fine, and a temporary cluster installed the old chart, upgraded, and rolled back. |
| `BLOCKED` | 1 | The report names a problem with this upgrade. The job fails. |
| `NOT CHECKED` | 2 | The check did not finish. The job fails. This says nothing about whether the upgrade is safe. |

A CI script that fails on any non-zero exit does the right thing. `PASSED` is green on purpose: the files were checked, and the report says the upgrade itself was not started.

`--require-rehearsal` means "fail unless the temporary cluster actually ran the upgrade." With that flag, a file-only check becomes `NOT CHECKED` (exit 2).

## Try the upgrade on a temporary cluster

```bash
stageproof check-pr --current-git "$BASE" --current-k8s 1.35 --live
```

`--live` creates a temporary [kind](https://kind.sigs.k8s.io/) cluster on the machine running the command, installs the chart you run today, upgrades it, rolls it back, and deletes the cluster. Nothing is committed. Two checks can run at the same time. Pass `--cluster-name` to choose the name.

`--live` starts a real Kubernetes node. That node is a container, and it has to set up its own networking and start more containers. An unprivileged container is blocked from doing those things, so kind has nothing to run the node with. A CI job that cannot start containers stays on the file check, and the report says `NOT TESTED LIVE`.

The temporary cluster is empty. It has none of your data, secrets, or private images. A chart that is already healthy in production can sit there until the 10 minute wait and still be a good chart. The report says when that happened.

Same flags on two chart directories, when there is no pull request:

```bash
stageproof check \
  --current-chart ./chart-v1 \
  --new-chart ./chart-v2 \
  -f values-prod.yaml \
  --current-k8s 1.35 \
  --live
```

Two versions in a Helm repository or an OCI registry. The charts are downloaded and removed. Without `--repo`, `--chart`, `--from`, and `--to` are still names for local chart directories.

```bash
stageproof check \
  --repo https://charts.jetstack.io \
  --chart cert-manager \
  --from v1.21.2 \
  --to v1.21.3 \
  --current-k8s 1.35
```

## What it looks at

| Question | How |
|---|---|
| Does the new chart render with your values? | `helm template` on Helm 3 or Helm 4. `--helm-compare` renders with a second helm binary and reports differences. When the current chart does not render because a default is empty, a schema field is null, the schema document is invalid, or a template names a missing API, the same placeholder is used on both charts and the report names the keys. |
| Does a values key you set still have an effect? | Reads templates and proves uncertain keys by rendering (`--values-proof default` or `full`). A key the old chart uses and the new chart ignores is `BLOCKED`. |
| Does a new or changed image tag exist for your node architecture? | HTTP HEAD to the image registry named in the render (skip with `--offline`). Checks `linux/amd64` or every node architecture in `--cluster-facts`. |
| Does the chart use an API your Kubernetes version removed? | A local Kubernetes API server of `--current-k8s` (`--api-server auto`). Otherwise a built-in table (`--api-server off`). |
| Will the next Kubernetes version remove an API the chart still uses? | `--next-k8s` prints a warning. The pull request can still merge. The cluster is not upgraded. |
| Can the upgrade be applied on top of the old release? | The local API server applies the current objects and dry-runs the new ones. Immutable selectors, immutable Job pod templates, StatefulSet `volumeClaimTemplates`, and invalid CRD updates are `BLOCKED`. A deleted PersistentVolumeClaim, Namespace, or templated CRD is `BLOCKED` unless `helm.sh/resource-policy: keep` is set. Restarts, privilege changes, and exposure changes are named without blocking. |
| Does a rendered manifest tree change safely? | `stageproof check --before <path> --after <path>` skips Helm and runs the API server, impact, and API compatibility checks directly. |
| What does my real cluster already run? | `stageproof facts export` reads server version, CRDs, admission policies, StorageClasses, and optional Helm release metadata (`get` and `list` only, no values). `--cluster-facts` feeds that file into `check` and `check-pr`. |
| Can I control noise and output in CI? | `stageproof explain <rule-id>`, structured summary sections, `--format sarif`, and `.stageproof.yaml` `ignores`, `policy`, and live `smoke` commands read from the base ref. `stageproof comment` posts or updates one pull request comment from `--comment-file`. `stageproof mcp` serves `check`, `check_pr`, and `explain` on stdin and stdout. |
| Does the new release start, and can it roll back? | `--live`, on the temporary kind cluster above, including `smoke` commands from `.stageproof.yaml`. |

`check-pr` finds every changed chart, every changed Argo CD Application, and every changed Flux HelmRelease, by reading `git diff`. A values file that an Application names is part of that check, including a file outside the chart directory. A source object or a `valuesFrom` file that a HelmRelease names is part of that check too. It does not ask Renovate, GitHub, or GitLab what changed. One Application on its own is `stageproof check --argocd-file`. One HelmRelease on its own is `stageproof check --flux-file`. `stageproof explain <rule-id>` prints what one rule means. `stageproof facts export` writes the server version, custom resource definitions, and installed chart versions from a kubeconfig you pass. `get` and `list` only. `--cluster-facts` reads that file. When a report has a delete, a CRD change, a privilege change, an exposure change, a values finding, a restart, or a check that did not finish, those sections are printed after the Kubernetes line. An empty section is left out. `--format sarif` prints a SARIF 2.1.0 log. `check-pr` reads `.stageproof.yaml` from the base ref. An ignore there keeps the finding and does not block.

## Install

Helm 3 or Helm 4 is required for every check. The commands Stageproof runs were checked on Helm 3.22.1 and Helm 4.3.2. Git is required for a pull request. `--live` also needs kind, kubectl, the Docker client, and a running Docker daemon. The `v0.3.1-live` image includes kind, kubectl, and the Docker client. The daemon is the one already running on the machine. A binary you installed yourself uses kind, kubectl, and Docker from that machine.

### Container image

`v0.3.1` publishes two images. `ghcr.io/stageproof/stageproof:v0.3.1` contains Stageproof, Helm 3.22.1, and Git. `ghcr.io/stageproof/stageproof:v0.3.1-live` adds kind 0.33.0, kubectl 1.35.10, and the Docker 29.9.2 client. A file check from a checkout:

```bash
docker run --rm -v "$PWD:/work" -w /work \
  ghcr.io/stageproof/stageproof:v0.3.1 \
  check-pr --current-git "$BASE" --current-k8s 1.35
```

To print the version:

```bash
docker run --rm ghcr.io/stageproof/stageproof:v0.3.1 version
```

`--live` uses the `v0.3.1-live` image. kind starts the temporary cluster by asking the machine's Docker daemon to run it, so the command gets the Docker socket. The temporary cluster's API is on `127.0.0.1`, so the container uses the host network. The image user is uid 65532, and the socket belongs to root, so the command runs as root.

```bash
docker run --rm --network host --user 0 \
  --security-opt label=disable \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:/work" -w /work \
  ghcr.io/stageproof/stageproof:v0.3.1-live \
  check-pr --current-git "$BASE" --current-k8s 1.35 --live
```

`--security-opt label=disable` is what a machine with SELinux enforcing needs. Other machines accept the flag.

A CI job that is not allowed to start containers stays on the file check. The report says `NOT TESTED LIVE`.

### Homebrew

```bash
brew install stageproof/stageproof/stageproof
```

`--live` with this binary uses kind, kubectl, and Docker installed on the machine.

### Helm plugin

Helm 3 and Helm 4 can run the same checks. The plugin downloads the `v0.3.1` archive and checks `checksums.txt` before it runs the binary. `helm stageproof` takes the same arguments as `stageproof` and returns the same exit code.

```bash
helm plugin install https://github.com/stageproof/stageproof
```

Helm 4 will not install a git plugin unless signature verification is turned off. A git checkout has no `.prov` file.

```bash
helm plugin install https://github.com/stageproof/stageproof --verify=false
```

```bash
helm stageproof version
```

```text
stageproof v0.3.1 (Apache-2.0)
```

```bash
helm stageproof check --current-chart current/metrics-server --new-chart new/metrics-server
```

Set `STAGEPROOF_VERSION` to another `vX.Y.Z` release before `helm plugin install` or `helm plugin update stageproof` when you want a different binary. The default is `v0.3.1`.

### GitHub release

Download the archive for your OS from the [releases](https://github.com/stageproof/stageproof/releases), then check the signature. The commands are in [Check a download](#check-a-download).

### From source

```bash
go install github.com/stageproof/stageproof/cmd/stageproof@v0.3.1
```

## What it leaves alone

Your cluster stays as it is. You pass the Kubernetes version it runs.

Kustomize and Helmfile are skipped. A Flux HelmRelease is read, and two rendered manifest trees are compared with `--before` and `--after`. `check-pr` still ignores a loose manifest that is not inside a chart. Terraform providers, the inside of a container image, and language packages are skipped. A new or changed image tag is asked of the registry named in that reference. `--offline` skips that ask and does not change `--api-server`. `--helm-compare` renders with a second helm binary. Charts stay on the machine that runs the command.

Uncertain values keys are proven by rendering (`--values-proof default`, or `full` for every key). The API server proof is a local process of the minor you name, not your cluster. Deprecations still come from a table. Both limits are in the [documentation](https://stageproof.github.io/stageproof/docs.html).

## Check a download

The release signs `checksums.txt` and the image with the GitHub Actions identity. No maintainer key is stored. These commands were run against the pre-release `v0.2.0-rc.1`. A tag with a hyphen does not move `latest`.

```bash
gh release download v0.2.0-rc.1 \
  -p checksums.txt -p checksums.txt.pem -p checksums.txt.sig \
  -p stageproof_0.2.0-rc.1_linux_amd64.tar.gz \
  -p stageproof_0.2.0-rc.1_linux_amd64.tar.gz.sbom.json

cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/stageproof/stageproof/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt

cosign verify \
  ghcr.io/stageproof/stageproof@sha256:2ba858d17b672dde9b610458220a20beec9f7d3b3fa153c39c5a671c84b043b0 \
  --certificate-identity "https://github.com/stageproof/stageproof/.github/workflows/release.yml@refs/tags/v0.2.0-rc.1" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"

gh attestation verify stageproof_0.2.0-rc.1_linux_amd64.tar.gz --repo stageproof/stageproof
```

The `.sbom.json` file is an SPDX document produced by syft. It names the module `github.com/stageproof/stageproof` at `v0.2.0-rc.1`.

## Documentation

The guides are in [`web/`](web/) and are published at <https://stageproof.github.io/stageproof/>.

The scope, in one page, is [docs/scope.md](docs/scope.md). Measured accuracy on 12 active incidents and 316 CNCF upgrade pairs across 105 CNCF projects (0/281 wrong blocks, p95 4.13s file check and 15.41s API server proof on `ubuntu-latest`) is in [docs/accuracy.md](docs/accuracy.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
