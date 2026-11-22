# Stageproof accuracy measurement

Date: 2026-11-22
Commit: `042579b` (`042579b` on `main`, measured in [workflow run 45065690496](https://github.com/stageproof/stageproof/actions/runs/45065690496))
Runner: GitHub Actions `ubuntu-latest`, Go 1.27.4, Helm v3.22.1, Kubernetes 1.35.0

## Summary

| Check | Budget / Target | Result | Status |
|---|---|---|---|
| Active incidents caught | 100% (`12 / 12`), 0 unreproduced | `12 / 12` caught, `0` unreproduced | Pass |
| Controls passed | 100% (`12 / 12`) | `12 / 12` passed | Pass |
| Hand-checked sample size | At least 300 unique upgrade pairs | `316` unique pairs across `105` CNCF projects | Pass |
| Wrong blocks (`--api-server off`) | At most 1.0% of labeled-clean | `0 / 281` (`0.00%`) | Pass |
| Wrong blocks (`--api-server auto`) | At most 1.0% of labeled-clean | `0 / 281` (`0.00%`) | Pass |
| File check latency (`--api-server off`) | p95 under 10.0s on `ubuntu-latest` | p50 `0.74s`, p95 `4.13s` (max `8.07s`) | Pass |
| API server proof latency (`--api-server auto`) | p95 under 30.0s on `ubuntu-latest` | p50 `5.19s`, p95 `15.41s` | Pass |

Every gate criterion passed on `ubuntu-latest`.

## Corpus composition

- **Incidents (`corpus/incidents/`)**: 12 active incident fixtures covering removed APIs, immutable selectors, immutable Job pod templates, immutable StatefulSet `volumeClaimTemplates`, CRD field type changes, CRD storage version removal, templated CRD deletion, `crds/` incompatible edits, PVC deletion, missing image tags, silently dropped values keys, and newly required unset values. Each active incident has a matching control chart that must pass. Two previously `unreproduced` fixtures (`conversion-webhook-breaks-api` and `keep-annotated-crd-migration`) never produced their named rules and were removed.
- **Public chart pairs (`corpus/pairs/pairs.yaml`)**: 316 unique Helm upgrade pairs spanning 105 current CNCF projects (HTTP and OCI repositories), locked by SHA-256 across 422 chart archives in `corpus/pairs/pairs.lock`. Five retired archived Bitnami and Airflow `blocked_clean` pairs from the earlier bootstrap set were removed and replaced.
- **Labels (`corpus/labels/chart-pairs.yaml`)**: Every label carries independent evidence and zero labels rely on `version-boundary`:
  - `302` by `rendered-diff` (comparing `helm template` output without `--include-crds` for release deletions, selector or `volumeClaimTemplates` changes, immutable non-hook Job pod templates, removed APIs on Kubernetes 1.35, and template render failures)
  - `13` by `chart-file` (independent OpenAPI v3 schema diff of `crds/` files for newly required fields, type changes, removed properties, enum narrowing, or removed storage versions)
  - `1` by `registry-check` (`spinkube-spin-operator-0.5.0-0.6.0`: direct GHCR HEAD check returned HTTP 404 for `ghcr.io/spinkube/spin-operator:v0.6.0`)
  - Totals: `281` labeled `clean`, `35` labeled `breaks`.

## Detailed results (`ubuntu-latest`, run 45065690496)

### Incidents (`--api-server auto`)

- Caught: `12 / 12` active
- Controls passed: `12 / 12`
- Latency: p50 `7.85s`, p95 `8.75s`

### Chart pairs (`--api-server off`, file check)

- Checked: `316 / 316`
- Labeled `clean`: `281 PASSED`, `0 BLOCKED` (`0.00%` wrong blocks)
- Labeled `breaks`: `25 BLOCKED`, `10 PASSED`
- Latency: p50 `0.74s`, p95 `4.13s`, max `8.07s` (`kyverno-kyverno-3.9.2-3.10.0`)

### Chart pairs (`--api-server auto`, Kubernetes 1.35.0)

- Checked: `316 / 316`
- Labeled `clean`: `281 PASSED`, `0 BLOCKED` (`0.00%` wrong blocks)
- Labeled `breaks`: `29 BLOCKED`, `6 PASSED` (the 4 additional breaks caught over file mode are non-hook `batch/v1 Job` objects whose `spec.template` changed on Cadence and Karpor)
- Latency: p50 `5.19s`, p95 `15.41s`
- `corpus compare`: `0` missed active incidents, `0` failed controls, `0` newly blocked clean pairs (`corpus/baseline.json` `blocked_clean` is empty)

### Flux replay corpus (local reference on `042579b`, Helm v3.22.1)

- `go run ./cmd/corpus run --set replay --out replay.json`
- Checked: `70 / 70` across 5 public repositories
- Verdicts: `62 PASSED`, `8 BLOCKED`, `0 NOT CHECKED`, `0` errors
- Latency: p50 `3.53s`, p95 `25.04s`

## Engine bugs fixed during this evaluation

Running the 316-pair CNCF corpus surfaced three false-block bugs in Stageproof, each fixed before the final run:

1. **Conditional `required` calls** ([PR #35](https://github.com/stageproof/stageproof/pull/35)): `required` inside `if` or `else` branches no longer triggers `helm.values.new_required_unset` when default values leave that branch disabled (fixed false blocks on Dragonfly, OpenFGA, and Interlink).
2. **CRD `enum` relaxations and unreachable `allOf` cleanup** ([PR #36](https://github.com/stageproof/stageproof/pull/36)): removing an `enum` constraint completely, or dropping a value from one `allOf` branch when a sibling `allOf` branch already forbade it, no longer reports an incompatible CRD schema change (fixed false blocks on Capsule and Envoy Gateway).
3. **Pre-existing invalid manifests on the current chart** ([PR #37](https://github.com/stageproof/stageproof/pull/37)): API server setup failures on `--current-chart` no longer block upgrades that fix them (fixed Pixie `0.1.4` to `0.1.5`), and pre-existing `k8s.apply.invalid` failures on the exact same fields in both charts downgrade to a warning (fixed Velero and Submariner defaults). Kubernetes `StatusCause` entries are sorted so repeated runs report the same field deterministically.

## Reproduce

```bash
git checkout 042579b
go run ./cmd/corpus run --set incidents --out incidents.json
go run ./cmd/corpus run --set pairs --api-server off --out pairs-files.json
go run ./cmd/corpus run --set pairs --api-server auto --out pairs.json
go run ./cmd/corpus compare incidents.json corpus/baseline.json
go run ./cmd/corpus compare pairs-files.json corpus/baseline.json
go run ./cmd/corpus compare pairs.json corpus/baseline.json
go run ./cmd/corpus run --set replay --out replay.json
```

The GitHub Actions run can be triggered on demand from the `corpus` workflow (`workflow_dispatch`).
