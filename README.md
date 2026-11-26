# Stageproof

**Free, open-source (Apache-2.0) single binary that checks a Kubernetes upgrade before you merge it.**

No account. No cluster credentials. **0 AI calls.** A config file is only for listing the values files a pull request should check.

Stageproof compares the Helm chart, Argo CD Application, Flux HelmRelease, or rendered YAML you run today with the version you are about to merge. Pass your values with `-f`, or let Argo CD and Flux use the values files those manifests already name. It runs a local Kubernetes API server on your machine and exits `0`, `1`, or `2`.

[Website & Docs](https://stageproof.github.io/stageproof/) · [300-Chart Public Index](https://stageproof.github.io/index/) · [GitHub Action](https://github.com/stageproof/action) · [Accuracy Report](docs/accuracy.md)

## Try it in 30 seconds (zero setup)

Install one binary:

```bash
brew install stageproof/stageproof/stageproof
```

(Or install as a [Helm plugin](#helm-plugin), run via [Docker](#container-image), or download a [signed release binary](#signed-release-binary).)

Scan any chart upgrade right away, with no checkout, no config, and no Kubernetes cluster:

```bash
stageproof check \
  --repo https://ygqygq2.github.io/charts/ \
  --chart nacos --from 2.1.9 --to 2.1.10 \
  --current-k8s 1.35
```

```text
stageproof  BLOCKED  exit 1
compare  nacos@2.1.9 -> nacos@2.1.10
k8s  1.35  ·  checked: files, API server 1.35.0

  ✓ PASS  helm template            rendered 17 Kubernetes objects cleanly
  ✓ PASS  values.yaml AST          no custom -f overlay passed (default chart values verified)
  ! WARN  upgrade signals          2 named changes
  ✓ PASS  image tags               1 new or changed image tag checked
  ✕ FAIL  API server 1.35.0        1 rejection
  ✓ PASS  impact                   Compared stored objects for 14 of 17 objects, and rendered objects for the rest.
  ✓ PASS  k8s 1.35 compatibility   current Kubernetes was checked by API server 1.35.0

  ERROR  k8s.apply.immutable_field
         Job/default/stageproof-rel-nacos-init-db
         Kubernetes rejected the update. `spec.template` is immutable.

verdict  BLOCKED (exit 1)  · 0 AI calls
```

The flag for your values file is `-f`. `--values` is the same flag. Repeat it for more than one file. Drop that line to check chart defaults:

```bash
stageproof check \
  --repo https://kubernetes-sigs.github.io/metrics-server/ \
  --chart metrics-server \
  --from 3.12.2 --to 3.13.0 \
  --current-k8s 1.35 \
  -f values.yaml # your values file
```

Or, from inside a git checkout, check the pull request:

```bash
# Reads the git diff. Does not contact your cluster.
# A chart uses its own values.yaml until .stageproof.yaml lists your files.
# Argo CD and Flux use the values files those manifests already name.
stageproof check-pr \
  --current-git origin/main \
  --current-k8s 1.35 \
  --next-k8s 1.36
```

`check-pr` checks every changed Helm chart, Argo CD Application, and Flux HelmRelease. It does not guess a production values file. A chart is rendered with its own `values.yaml` until `.stageproof.yaml` lists the files you deploy. `stageproof init` writes that list: `values.yaml`, `values-*.yaml`, and files in `ci/`. The GitHub Action and the GitLab component run this same command. They have no separate values input. Your production cluster is never contacted.

## Why `helm lint` is not enough

`helm lint` and `helm template` inspect the new chart on its own. Real outages happen in the diff between what is running and what you are about to apply:

- **Silently dropped values:** The new chart renames or removes a key from your `values.yaml`. Helm ignores it without error, and your production setting disappears.
- **Immutable fields:** A changed Deployment selector, Job pod template, or StatefulSet `volumeClaimTemplates` works on a fresh install and fails on upgrade. Changing `strategy.type` from RollingUpdate to Recreate while `rollingUpdate` is still set fails the same way. The message names that conflict. Recreate to RollingUpdate without `maxUnavailable` or `maxSurge` does too.
- **Removed Kubernetes APIs & broken CRDs:** Objects your cluster minor no longer serves, or CRD schema changes that break existing custom resources.
- **Accidental deletes & missing images:** Dropped PVCs, Namespaces, or templated CRDs, and container image tags that do not exist in the registry for your node architecture.

Measured on GitHub `ubuntu-latest` (2026-11-25, [docs/accuracy.md](docs/accuracy.md), [workflow 45519144615](https://github.com/stageproof/stageproof/actions/runs/45519144615)): **18 / 18** real incidents caught, **18 / 18** clean controls passed, **0 / 283** wrong blocks across 316 upgrade pairs from 105 CNCF projects. Median file check: `0.92s` (p95 `3.47s`). Median API server proof: `5.42s` (p95 `13.71s`).

## Drop into CI (free)

### GitHub Actions

```yaml
# Same check as: stageproof check-pr
# No values-file input. A chart uses its own values.yaml until
# .stageproof.yaml lists your files. Argo CD and Flux use the
# values files those manifests name.
# current-k8s is the Kubernetes version you deploy to.
name: stageproof
on:
  pull_request:
permissions:
  contents: read
  pull-requests: write
jobs:
  check:
    runs-on: ubuntu-latest
    steps:
      - uses: stageproof/action@v1
        with:
          current-k8s: "1.35"
```

The action checks out the pull request, downloads the signed `v0.5.1` binary, checks `checksums.txt`, runs `stageproof check-pr`, writes the job summary, and posts or updates one PR comment. Values follow the `check-pr` rules above. Safe on fork PRs (do not use `pull_request_target`). See [stageproof/action](https://github.com/stageproof/action).

### GitLab CI

```yaml
# Same check as: stageproof check-pr
# No values-file input. A chart uses its own values.yaml until
# .stageproof.yaml lists your files. Argo CD and Flux use the
# values files those manifests name.
# current_k8s is the Kubernetes version you deploy to.
# A merge request note needs STAGEPROOF_GITLAB_TOKEN.
include:
  - component: gitlab.com/zarovvvv/stageproof/stageproof@1
    inputs:
      current_k8s: "1.35"
```

Uses the [zarovvvv/stageproof](https://gitlab.com/explore/catalog/zarovvvv/stageproof) CI/CD catalog component. Same values rules as `check-pr`. `CI_JOB_TOKEN` cannot write merge request notes. The check still runs, and the report is a job artifact. Manual job setup is in the [GitLab docs](https://stageproof.github.io/stageproof/docs.html#ci-gitlab).

### Renovate upgrade badges & public 300-chart index

Browse chart-default upgrade reports for 300 popular public Helm charts (21,000+ upgrade pairs) at the free [Stageproof Index](https://stageproof.github.io/index/). Add one line to `renovate.json` to show an upgrade badge on Helm PRs:

```json
{
  "extends": ["github>stageproof/renovate-presets"]
}
```

The badge is that index report. It uses chart defaults, not the values file you deploy. Your values are checked by `stageproof check -f` or by `check-pr` after `.stageproof.yaml` lists them. See the [Renovate guide](https://stageproof.github.io/stageproof/docs.html#renovate) for the patch auto-merge preset.

## Verdicts & exit codes

| Verdict | Exit | Meaning |
|---|---|---|
| `PROVEN` | `0` | File checks passed, and a temporary `kind` cluster installed, upgraded, and rolled back cleanly (`--live`). |
| `PASSED` | `0` | All file and local API server checks passed. No live cluster was started. CI stays green. |
| `BLOCKED` | `1` | A breaking upgrade problem was found. The report names the rule and object. CI fails. |
| `NOT CHECKED` | `2` | The check could not finish (or `--require-rehearsal` was set without kind). CI fails. |

## Common commands

```bash
# 1. Compare two local chart directories with your values
stageproof check --current-chart ./chart-v1 --new-chart ./chart-v2 -f values-prod.yaml --current-k8s 1.35

# 2. Compare two versions from any Helm repo or OCI registry
stageproof check --repo https://charts.jetstack.io --chart cert-manager --from v1.21.2 --to v1.21.3 --current-k8s 1.35

# 3. Check an Argo CD Application or Flux HelmRelease
stageproof check --argocd-file apps/ingress.yaml --current-k8s 1.35
stageproof check --flux-file clusters/prod/redis.yaml --current-k8s 1.35

# 4. Compare already-rendered YAML trees (no Helm required)
stageproof check --before ./rendered-old --after ./rendered-new --current-k8s 1.35

# 5. Pull request check. A chart uses values.yaml until .stageproof.yaml lists your files.
stageproof check-pr --current-git origin/main --current-k8s 1.35 --live
```

Also built into `v0.5.1`:
- `stageproof facts export`: read-only export of your cluster's Kubernetes version, CRDs, and node architectures (`--cluster-facts`).
- `stageproof explain <rule-id>`: plain explanation and fix for any rule.
- `--helm-compare`: compare Helm 3 and Helm 4 renders side by side.
- `--format sarif` / `--format json` / `--comment-file` / `stageproof comment`: CI integration.
- `.stageproof.yaml`: timed ignores, severity policy, and live `smoke` commands (read from the base ref on PRs).
- `stageproof mcp`: MCP server (`check`, `check_pr`, `explain`) on stdin/stdout for coding agents.

## Install

Requirements: **Helm 3 or Helm 4** (tested on 3.22.1 and 4.3.2) for chart checks. **Git** for `check-pr`. Optional **kind, kubectl, Docker** only if you pass `--live`.

### Homebrew

```bash
brew install stageproof/stageproof/stageproof
```

### Helm plugin

```bash
# Helm 3
helm plugin install https://github.com/stageproof/stageproof

# Helm 4 (git checkout has no .prov file)
helm plugin install https://github.com/stageproof/stageproof --verify=false

helm stageproof version
```

### Container image

`ghcr.io/stageproof/stageproof:v0.5.1` bundles Stageproof, Helm 3.22.1, and Git. `ghcr.io/stageproof/stageproof:v0.5.1-live` adds kind, kubectl, and the Docker client.

```bash
docker run --rm -v "$PWD:/work" -w /work \
  ghcr.io/stageproof/stageproof:v0.5.1 \
  check-pr --current-git origin/main --current-k8s 1.35
```

With `--live` (uses the host Docker daemon to start and delete a temporary `kind` cluster):

```bash
docker run --rm --network host --user 0 \
  --security-opt label=disable \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$PWD:/work" -w /work \
  ghcr.io/stageproof/stageproof:v0.5.1-live \
  check-pr --current-git origin/main --current-k8s 1.35 --live
```

### Signed release binary

Download the archive for Linux, macOS, or Windows (`amd64` / `arm64`) from [GitHub Releases](https://github.com/stageproof/stageproof/releases), or install with Go:

```bash
go install github.com/stageproof/stageproof/cmd/stageproof@v0.5.1
```

### Verify a release download

Every release signs `checksums.txt` and the container images with keyless GitHub Actions OIDC (Sigstore Cosign) and publishes SPDX SBOMs:

```bash
gh release download v0.5.1 -R stageproof/stageproof \
  -p checksums.txt -p checksums.txt.pem -p checksums.txt.sig \
  -p stageproof_0.5.1_linux_amd64.tar.gz

cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity "https://github.com/stageproof/stageproof/.github/workflows/release.yml@refs/tags/v0.5.1" \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  checksums.txt

sha256sum -c --ignore-missing checksums.txt
gh attestation verify stageproof_0.5.1_linux_amd64.tar.gz --repo stageproof/stageproof
```

## What it does not do

- Never modifies your repository or contacts your production cluster.
- Never sends your charts or values to an external Stageproof service or AI model. Pass `--offline` to skip container registry HEAD checks and `--api-server off` to skip downloading local `kube-apiserver` binaries.
- Built for Kubernetes upgrades (Helm, Argo CD, Flux, rendered YAML). Does not check Terraform providers, container image internals, Kustomize directly, or language packages. Full boundaries: [docs/scope.md](docs/scope.md).

## Documentation & License

- Full guides: <https://stageproof.github.io/stageproof/>
- Scope & limits: [docs/scope.md](docs/scope.md)
- Measured accuracy: [docs/accuracy.md](docs/accuracy.md)
- License: **Apache-2.0** ([LICENSE](LICENSE)). Free for personal and commercial use.
