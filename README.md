# Stageproof

Stageproof checks a Helm chart upgrade before you merge it.

It compares the chart you run today with the chart in the pull request, using your values and the Kubernetes version your cluster runs. It prints one result and exits 0, 1, or 2. It does not edit the pull request, and it does not change a cluster you already have.

```text
stageproof  PASSED  exit 0
compare  current/metrics-server -> new/metrics-server
k8s  1.33  ·  checked: files

  ✓ PASS  helm template            rendered 9 Kubernetes objects cleanly
  ✓ PASS  values.yaml AST          no custom -f overlay passed (default chart values verified)
  ✓ PASS  k8s 1.33 compatibility   0 removed APIs or selector breaks on K8s 1.33

verdict  PASSED (exit 0)  · 87ms · 0 AI calls
```

That run is metrics-server 3.12.2 to 3.13.0. `checked: files` means nothing was started.

## Install

Helm 3 is required for every check. Git is required for pull requests. kind, kubectl, and Docker are required only for `--live`.

**Homebrew**

```bash
brew install stageproof/stageproof/stageproof
```

**Container image.** The image contains Stageproof, Helm, and Git. It does not run `--live`, because that needs a Docker daemon.

```bash
docker run --rm -v "$PWD:/work" -w /work ghcr.io/stageproof/stageproof:v0.1.2 version
```

**GitHub release.** Download the archive for your OS from the [releases](https://github.com/stageproof/stageproof/releases), then check the signature. The commands are in [Verify a download](#verify-a-download).

**From source**

```bash
go install github.com/stageproof/stageproof/cmd/stageproof@v0.1.2
```

## Results

| Result | Exit | Meaning |
|---|---|---|
| `PROVEN` | 0 | The files passed, and a temporary cluster installed the old chart, upgraded, and rolled back. |
| `PASSED` | 0 | The files passed. No cluster was used, or the rehearsal stopped because of something outside the chart. |
| `BLOCKED` | 1 | The upgrade has a problem the report names. |
| `NOT CHECKED` | 2 | The check did not finish, or `--require-rehearsal` was set and no rehearsal passed. |

A CI script that fails on any non-zero status does the right thing. `PASSED` is green on purpose.

## What it checks

| Question | How |
|---|---|
| Does the new chart render with your values? | `helm template` |
| Does a values key you set still get read? | The chart templates. A key the old chart reads and the new chart does not is `BLOCKED`. |
| Does the chart use an API your Kubernetes version removed? | A table of removed APIs, at `--current-k8s`. |
| Will the next Kubernetes version remove an API the chart still uses? | `--next-k8s` prints a warning. It does not block, and it does not upgrade the cluster. |
| Can the upgrade be applied on top of the old release? | Label selectors that Kubernetes refuses to change are `BLOCKED`. |
| Does the new release start, and can it roll back? | Only with `--live`. |

`--live` creates a temporary kind cluster on the machine running the command, installs the old chart, upgrades, rolls back, and deletes that cluster.

An Argo CD Application that names one Helm chart is checked with `--argocd-file`. `stageproof check-pr` finds every changed chart and Application from `git diff`. It does not call Renovate or GitHub to ask what changed.

## What it does not do

It does not upgrade a Kubernetes cluster. You tell it the version the cluster runs.

It does not read Flux, Kustomize, Helmfile, or plain manifests. It does not check Terraform providers, container image contents, or language packages. It does not send your charts anywhere.

The values check reads templates. A key that is mentioned and then ignored can still look used. The API check is a table, not a live API server. Both limits are stated in [the documentation](https://stageproof.github.io/stageproof/docs.html).

## A pull request

```bash
stageproof check-pr \
  --current-git "$BASE" \
  --current-k8s 1.35 \
  --comment-file stageproof-comment.md \
  --out stageproof-report.json
```

Exit 0 is `PASSED` or `PROVEN`. Exit 1 and exit 2 fail the job. GitHub Actions and GitLab CI examples are in the documentation.

## Verify a download

The release signs `checksums.txt` with the GitHub Actions identity. No maintainer key is stored.

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/stageproof/stageproof/.github/workflows/release.yml@refs/tags/v0.1.2" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt
```

The container image is signed the same way:

```bash
cosign verify ghcr.io/stageproof/stageproof:v0.1.2 \
  --certificate-identity "https://github.com/stageproof/stageproof/.github/workflows/release.yml@refs/tags/v0.1.2" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com"
```

## Documentation

The guides are in [`web/`](web/) and are published at <https://stageproof.github.io/stageproof/>.

The scope, in one page, is [docs/scope.md](docs/scope.md).

## License

Apache-2.0. See [LICENSE](LICENSE).
