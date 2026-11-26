# Stageproof accuracy measurement

Date: 2026-11-25
Commit: `c3ec5b0` (`c3ec5b051d568f1a41081628d2c591bed54c39be` on `accuracy-labels`, measured in [workflow run 45519144615](https://github.com/stageproof/stageproof/actions/runs/45519144615))
Runner: GitHub Actions `ubuntu-latest`, Go 1.27.4, Helm v3.22.1, Kubernetes 1.35.0

`c3ec5b0` is `v0.4.0` (`06ce6f3`) plus the label corrections below. The render change is `a12b307`, which `v0.4.0` already contains. The released binary is v0.4.0.

## Summary

| Check | Budget / Target | Result | Status |
|---|---|---|---|
| Active incidents caught | 100% (`18 / 18`), 0 unreproduced | `18 / 18` caught, `0` unreproduced | Pass |
| Controls passed | 100% (`18 / 18`) | `18 / 18` passed | Pass |
| Hand-checked sample size | At least 300 unique upgrade pairs | `316` unique pairs across `105` CNCF projects | Pass |
| Wrong blocks (`--api-server off`) | At most 1.0% of labeled-clean | `0 / 283` (`0.00%`) | Pass |
| Wrong blocks (`--api-server auto`) | At most 1.0% of labeled-clean | `0 / 283` (`0.00%`) | Pass |
| File check latency (`--api-server off`) | p95 under 10.0s on `ubuntu-latest` | p50 `0.92s`, p95 `3.47s` (max `6.47s`) | Pass |
| API server proof latency (`--api-server auto`) | p95 under 30.0s on `ubuntu-latest` | p50 `5.42s`, p95 `13.71s` (max `66.23s`) | Pass |

Every gate criterion passed on `ubuntu-latest`. The API server maximum is one pair, `openfunction-openfunction-0.6.1-0.7.0`. The budget is the p95, which is under 30s.

## How labels are derived

A wrong block is a pair labeled `clean` whose verdict is `BLOCKED`. A pair labeled `breaks` that passes is not a wrong block.

The 2026-11-22 labels were made from the same install-mode `helm template` as the engine before `a12b307`. That render does not pass served kinds, so `Capabilities.APIVersions.Has "batch/v1/CronJob"` is false and the chart falls back to `batch/v1beta1`. It also renders install-only Jobs and misses upgrade hooks. Those labels cannot see that class of mistake.

On 2026-11-25 the chart templates for every `rendered-diff` pair were read for `Capabilities.APIVersions.Has` with a kind, and for `.Release.IsInstall`, `.Release.IsUpgrade`, and `.Release.Revision`. A label was re-derived when its evidence named an object from such a template. A `clean` label was checked in the other direction as well: an upgrade-only object the install render never compared. No other truth changed.

| Case | Was | Is | Why |
|---|---|---|---|
| `artifact-hub-artifact-hub-1.23.0-1.24.0` | `breaks` | `clean` | CronJobs `scanner` and `tracker` render `batch/v1` when the cluster serves `batch/v1/CronJob`. `batch/v1beta1` is only the fallback. The db migrator Job is `db-migrator-install` on install and a pre-upgrade hook `db-migrator-upgrade` otherwise. |
| `artifact-hub-artifact-hub-1.22.0-1.23.0` | `breaks` | `clean` | Same CronJob gate and the same install-versus-hook migrator Job. |
| `artifact-hub-artifact-hub-1.21.0-1.22.0` | `breaks` | `breaks` | `Chart.yaml` moves postgresql from `charts.helm.sh/stable` 8.2.1 (`postgresql.master.fullname`) to `charts.bitnami.com/bitnami` 18.0.15 (`postgresql.v1.primary.fullname`), so the old StatefulSet leaves the release. The `batch/v1beta1` line was the CronJob fallback and was removed from the evidence. v0.4.0 reports that delete as a warning, so this pair is `PASSED` in both modes. |
| `cadence-workflow-cadence-1.6.4-1.6.5`, `1.6.5-1.6.6`, `1.6.6-1.6.7` | `breaks` | `breaks` | `templates/schema-server-job.yaml` is a fixed-name Job, enabled by default, with no install, upgrade, or revision branch and no hook annotation. `cadence.labels` puts `helm.sh/chart` on the pod template, and that value changes with the chart version. |
| `kusionstack-karpor-0.7.5-0.7.6` | `breaks` | `breaks` | `templates/job.yml` is a fixed-name Job `karpor-generate-cert` with no install, upgrade, or revision branch and no hook annotation. The image tag defaults to `appVersion` with a `v` prefix, so it moves from `kusionstack/karpor:v0.6.3` to `kusionstack/karpor:v0.6.4`. |

Those seven labels use `by: template-read` and date `2026-11-25`. The other 309 labels are unchanged.

Other charts do contain those template patterns. Their label evidence does not name the gated object, so the truth stayed. Examples: Ingress and IngressClass gates, a PodSecurityPolicy rendered only when values and `policy/v1beta1/PodSecurityPolicy` are both set, VolumeSnapshotClass, GatewayClass, and Strimzi custom resources. `kuberhealthy-kuberhealthy-2.1.0-2.2.0` stays `breaks` because the new chart hardcodes `batch/v1beta1`, `policy/v1beta1`, and `rbac.authorization.k8s.io/v1beta1`. OpenEBS etcd `initialClusterState`, the Volcano admission annotation `rollme/helm-revision`, the Dragonfly sentinel templates, and the Istio validating webhook `failurePolicy` use a release field, and the same expression is on both sides of the pair or behind values that the default render does not enable.

### What the 2026-11-22 run would have shown

[Workflow run 45065690496](https://github.com/stageproof/stageproof/actions/runs/45065690496) on `042579b` published no blocked pair among the 281 it called clean, and 35 pairs labeled `breaks`. The two Artifact Hub pairs that are now `clean` were in that blocked set: their evidence was `removed API batch/v1beta1`, and that finding is a file-mode block. Scored with the labels in this document, that run would have been `2 / 283` wrong blocks (`0.71%`) in both modes. The Cadence and Karpor pairs stayed `breaks`, so they were not wrong blocks. This is a rescoring of the published counts, not a second execution of `042579b`.

## Corpus composition

- **Incidents (`corpus/incidents/`)**: 18 active incident fixtures. They cover removed APIs, a CronJob and a PodDisruptionBudget gated on the current kind, a PodSecurityPolicy guarded only by a legacy API version that `helm template` still lists, immutable selectors, immutable Job pod templates, install-only Jobs, upgrade-hook Jobs, revision-named Jobs beside a fixed-name Job, immutable StatefulSet `volumeClaimTemplates`, CRD field type changes, CRD storage version removal, templated CRD deletion, `crds/` incompatible edits, PVC deletion, missing image tags, silently dropped values keys, and newly required unset values. Each active incident has a matching control chart that must pass.
- **Public chart pairs (`corpus/pairs/pairs.yaml`)**: 316 unique Helm upgrade pairs spanning 105 current CNCF projects (HTTP and OCI repositories), locked by SHA-256 across 422 chart archives in `corpus/pairs/pairs.lock`.
- **Labels (`corpus/labels/chart-pairs.yaml`)**: Every label carries evidence, and zero labels rely on `version-boundary`:
  - `295` by `rendered-diff` (an independent `helm template` diff for release deletions, selector or `volumeClaimTemplates` changes, immutable non-hook Job pod templates, removed APIs on Kubernetes 1.35, and template render failures)
  - `7` by `template-read` (the chart templates, for the seven cases in the table above)
  - `13` by `chart-file` (independent OpenAPI v3 schema diff of `crds/` files)
  - `1` by `registry-check` (`spinkube-spin-operator-0.5.0-0.6.0`: direct GHCR HEAD check returned HTTP 404 for `ghcr.io/spinkube/spin-operator:v0.6.0`)
  - Totals: `283` labeled `clean`, `33` labeled `breaks`.

## Detailed results (`ubuntu-latest`, run 45519144615)

### Incidents (`--api-server auto`)

- Caught: `18 / 18` active
- Controls passed: `18 / 18`
- Latency: p50 `8.07s`, p95 `9.53s`

### Chart pairs (`--api-server off`, file check)

- Checked: `316 / 316`
- Labeled `clean`: `283 PASSED`, `0 BLOCKED` (`0.00%` wrong blocks)
- Labeled `breaks`: `22 BLOCKED`, `11 PASSED`
- Latency: p50 `0.92s`, p95 `3.47s`, max `6.47s` (`kyverno-kyverno-3.9.2-3.10.0`)

### Chart pairs (`--api-server auto`, Kubernetes 1.35.0)

- Checked: `316 / 316`
- Labeled `clean`: `283 PASSED`, `0 BLOCKED` (`0.00%` wrong blocks)
- Labeled `breaks`: `26 BLOCKED`, `7 PASSED`
- The 4 additional blocks over file mode are the three Cadence schema-server Jobs and the Karpor `karpor-generate-cert` Job. File mode does not apply those objects, so it does not see the immutable pod template.

### v0.5.0 file-mode Job templates

v0.4.0 API mode blocked 26 of 33 labeled breaks and passed 7. File mode passed those 7 and 4 more. The 4 are the Cadence schema Jobs and the Karpor cert Job. v0.5.0 compares `spec.template` on a non-hook Job when the API server does not run, which is the same rejection the API server already returns. A Helm hook is skipped. A template that uses `.Release.Revision` in the object name stays a warning. File mode still does not compare StatefulSet `volumeClaimTemplates`. The API server does, and `statefulset-volume-claim-templates-changed` stays an API-mode catch.

A local file-mode run of the 33 labeled breaks after that change blocked 26 and passed the same 7 the API-mode measurement passed:

| Case | Why it stays `PASSED` |
|---|---|
| `artifact-hub-artifact-hub-1.21.0-1.22.0` | The StatefulSet that leaves the release is a warning. |
| `rook-rook-ceph-v1.20.10-1.21.0` | CRD schema edits on rendered CRDs. No rendered object uses the changed field, so the finding stays a warning. |
| `volcano-volcano-1.15.4-1.16.0` | Same rendered-CRD schema warning. |
| `koordinator-koordinator-1.7.0-1.8.0` | Same rendered-CRD schema warning. |
| `keda-keda-2.20.2-2.21.0` | Same rendered-CRD schema warning. |
| `keda-keda-2.20.1-2.20.2` | Same rendered-CRD schema warning. |
| `linkerd-linkerd-crds-1.6.1-1.8.0` | Same rendered-CRD schema warning. |

CRDs under `crds/` stay `crd.not_upgraded_by_helm` blockers. A new blocker for every rendered schema edit would also flag pairs the 2026-11-25 run called clean. That check is not added.

v0.5.0 also explains a RollingUpdate to Recreate conflict, and a Recreate to RollingUpdate conflict, instead of calling `spec.strategy.rollingUpdate` immutable. An upgrade-only password check (`'path' must not be empty`) is filled on both charts so `--is-upgrade` can render. A path named inside a subchart template is prefixed with that dependency. Corpus run `45728461638` on the v0.5.0 tree measured 0/283 wrong blocks in file mode and in API mode. The incidents step passed. v0.5.1 prefixes a password path that a subchart template names. Corpus run `45740702239` on that tree measured 0/283 wrong blocks in file mode and in API mode. The incidents step passed. v0.5.2 prefixes every dependency directory in a nested template path. Camunda platform 8.3.24 to 8.3.25 and Artifactory OSS 107.111.10 to 107.111.11 stayed PASSED, and the upgrade fallback on those pairs was gone.
- Latency: p50 `5.42s`, p95 `13.71s`, max `66.23s` (`openfunction-openfunction-0.6.1-0.7.0`)
- `corpus compare`: `0` missed active incidents, `0` failed controls, `0` newly blocked clean pairs (`corpus/baseline.json` `blocked_clean` is empty)

### Flux replay

This run did not execute the Flux replay. The corpus workflow runs incidents and the 316 pairs. The last local replay, on `042579b` with Helm v3.22.1, checked `70 / 70` (`62 PASSED`, `8 BLOCKED`, `0 NOT CHECKED`, `0` errors). That figure is not part of run 45519144615.

## Engine bugs fixed before the 2026-11-22 run

The 316-pair corpus on `042579b` had already included three fixes. They are still in this tree:

1. **Conditional `required` calls** ([PR #35](https://github.com/stageproof/stageproof/pull/35)): `required` inside `if` or `else` branches no longer triggers `helm.values.new_required_unset` when default values leave that branch disabled.
2. **CRD `enum` relaxations and unreachable `allOf` cleanup** ([PR #36](https://github.com/stageproof/stageproof/pull/36)): removing an `enum` constraint completely, or dropping a value from one `allOf` branch when a sibling `allOf` branch already forbade it, no longer reports an incompatible CRD schema change.
3. **Pre-existing invalid manifests on the current chart** ([PR #37](https://github.com/stageproof/stageproof/pull/37)): API server setup failures on `--current-chart` no longer block upgrades that fix them, and pre-existing `k8s.apply.invalid` failures on the exact same fields in both charts downgrade to a warning.

`v0.4.0` then changed rendering ([PR #59](https://github.com/stageproof/stageproof/pull/59), [PR #60](https://github.com/stageproof/stageproof/pull/60), [PR #61](https://github.com/stageproof/stageproof/pull/61), commit `a12b307`). Served API kinds are passed to `helm template`, the new chart is rendered with `--is-upgrade`, and an immutable-field finding on a name that includes `.Release.Revision` is a warning. This document measures that engine against the corrected labels.

## Reproduce

```bash
git checkout c3ec5b051d568f1a41081628d2c591bed54c39be
go run ./cmd/corpus run --set incidents --out incidents.json
go run ./cmd/corpus run --set pairs --api-server off --out pairs-files.json
go run ./cmd/corpus run --set pairs --api-server auto --out pairs.json
go run ./cmd/corpus compare incidents.json corpus/baseline.json
go run ./cmd/corpus compare pairs-files.json corpus/baseline.json
go run ./cmd/corpus compare pairs.json corpus/baseline.json
```

The numbers above are workflow run 45519144615. The full 316-pair set runs on `workflow_dispatch` or the schedule, not on a pull request. A pull request runs the incident set and the `--pr` pairs.
