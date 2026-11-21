# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Stageproof is not 1.0. The rules for what a version may change are in [docs/versioning.md](docs/versioning.md).

## [Unreleased]

The next release is 0.2.0.

### Added

- `--helm-compare` renders the chart with the helm on `PATH` and with a second helm binary. Parsed objects are compared. A field that changes between two renders of the same binary is ignored. A remaining difference is `helm4.render_differs`, a warning. A binary that cannot render the chart is `helm4.render_failed` and exits 1. `Chart.yaml` `apiVersion: v3` is `helm4.chart_v3`, a warning. The commands this program runs were checked on Helm 3.22.1 and Helm 4.3.2.
- A new or changed container image tag is asked of the registry named in that reference. HTTP HEAD checks that the tag exists. A manifest list is fetched only to see whether `linux/amd64` is present. Layers are not downloaded. A missing tag (`supply.image.not_found`) or a missing platform (`supply.image.platform_missing`) is `BLOCKED`. A 401 or 403 is `supply.image.unverifiable` and does not block. A timeout, a rate limit, or a server error is `supply.registry_unreachable` and does not block. `supply.image.moved` names a repository change. `deprecated: true` in the new `Chart.yaml` is `supply.chart.deprecated`, a warning, including with `--offline`. `--offline` skips the registry and does not change `--api-server`. An unchanged image is not asked. This uses `github.com/google/go-containerregistry` v0.20.6. `github.com/klauspost/compress` is v1.20.2. A build of this commit is 61 MB.
- Values keys the template reader cannot decide are proven by rendering. Stageproof renders with the value, without it, and with another value of the same type when yours matches the chart default. A key with an effect in the current chart and none in the new chart stays `helm.values.silently_dropped`, now with `"proof": "render"`. A key read by an exact path stays `"proof": "templates"`. `--values-proof full` render-tests every key. The default is `default`. A YAML list is one key. Fields that change between two identical renders are ignored. A probe that `values.schema.json` rejects is not a finding. `helm.values.renamed` names a new key whose rendered effect matches, with `"verified": true`, and leaves a similar name unverified. `helm.values.default_changed` warns when a chart default you do not set still changes the render. The report names keys and chart defaults. It does not print your values. The check summary counts keys proven by rendering and keys read in templates.
- `stageproof check --before` and `--after` compare two rendered manifest files or directories. Helm is not called. Chart stages are recorded as skipped. The API server, the impact check, and the API table still run. `checked` is `rendered manifests`, or `rendered manifests, API server <patch>` when the API server ran. The report adapter is `manifests`. `--live`, values files, and `--set` with these flags exit 2. A path that cannot be read is `manifest.invalid_yaml`.
- An Argo CD Application check reports adapter `argocd`. A chart check stays `helm`. The Application's `skipCrds` is read. When the field is absent, `crds/` stays a warning that says so. `skipCrds: false` warns and says the CRDs are applied. `skipCrds: true` uses the plain Helm rule. `spec.sources`, `valueFiles`, `valuesObject`, `parameters`, `releaseName`, and a Git path in this checkout are read. `valuesObject` replaces `values`. A `$ref/` values file is read when that source is this repository. A missing values file is `NOT CHECKED` unless `ignoreMissingValueFiles` is true.
- A Flux HelmRelease check reports adapter `flux`. `helm.toolkit.fluxcd.io/v2`, `v2beta2`, and `v2beta1` are read. `spec.chart.spec` resolves a HelmRepository, an OCIRepository, or a GitRepository in this repository. `spec.chartRef` resolves an OCIRepository or a HelmChart in this repository. `spec.values` and `spec.valuesFrom` are applied in Flux order, and a `targetPath` entry overwrites earlier values. A ConfigMap or Secret that is a YAML file in the repository is a values file. A missing `valuesFrom` reference is `flux.values.unreadable`. The chart is still rendered and the result stays `PASSED`. `spec.upgrade.crds` is passed to the CRD check. When the field is omitted the policy is Skip, which is the helm-controller upgrade default. `check-pr` follows the HelmRelease, its source object, a `valuesFrom` file, and a chart path in this checkout.
- JSON reports start with `schema_version` 1. The field is added. Existing fields are unchanged.
- The accuracy corpus runs the incident fixtures and 100 public chart pairs. `go run ./cmd/corpus` is not in the release archives. `go run ./cmd/corpus run --set replay` checks the public Flux upgrades listed in `corpus/replay/cases.jsonl`. Each line is a repository, a commit, a path, and two chart versions. The command prints counts and timings.
- `--api-server auto` asks a local Kubernetes API server of the requested minor whether the upgrade is accepted. The binaries are `kube-apiserver`, `etcd`, and `kubectl` from the controller-tools envtest index, checked with the SHA-512 in that index, and cached on disk. `stageproof fetch-apiserver` downloads them for a machine that cannot fetch them later. The module now depends on `k8s.io` and `sigs.k8s.io`. A build of this commit is 57 MB.
- Rules `k8s.apply.immutable_field`, `k8s.apply.unknown_field`, `k8s.apply.invalid`, `crd.existing_object_invalid`, and `k8s.apply.not_exercised`.
- The report leads with what the upgrade deletes, restarts, exposes, or breaks. New rules: `impact.delete`, `impact.delete.crd`, `impact.delete.pvc`, `impact.delete.namespace`, `impact.delete.statefulset`, `impact.restart`, `impact.exposure.service`, `impact.exposure.route`, `impact.exposure.networkpolicy`, `impact.exposure.host`, `impact.privilege.rbac`, `impact.privilege.binding`, `impact.privilege.securitycontext`, `impact.privilege.hostpath`, `impact.privilege.webhook`, `impact.privilege.conversion`, `impact.capacity.replicas`, `impact.capacity.requests`, `impact.capacity.pdb`, `impact.capacity.hpa`, `crd.schema.incompatible`, `crd.scope_changed`, `crd.not_upgraded_by_helm`, `crd.argocd_renders_crds`, and `crd.flux_upgrade_crds`. `crd.existing_object_invalid` is also used when a storage version is removed or a custom resource uses an incompatible field. CRD schema comparison uses `sigs.k8s.io/crdify` v0.6.0. The module also depends on `k8s.io/apiextensions-apiserver`.
- When the API server did not run, the impact check says it compared the rendered objects. A restart is info. An object with `helm.sh/resource-policy: keep` is not reported as deleted.

### Changed

- The Helm release name for an Argo CD Application is `helm.releaseName`, or the Application name when that field is empty. Both revisions use that name. A chart check still uses `stageproof-rel`.

### Breaking

- With Helm 4 on `PATH`, an upgrade (a check that names a current chart) no longer treats an unknown field as a server-side apply failure. Helm 4 keeps the previous apply method, and a release created by Helm 3 stays client-side. A check with no current chart still treats Helm 4 as a new install, which uses server-side apply. `--server-side-apply` is unchanged. The same upgrade can go from exit 1 to exit 0.
- `--helm-compare` exits 1 when the other helm binary cannot render the chart. The same chart without the flag is unchanged.
- A new or changed image tag that the registry says is missing, or a manifest list with no `linux/amd64` entry, is now `BLOCKED` (exit 1). The same chart used to exit 0. A 401, a 403, a timeout, or a rate limit stays exit 0. `--offline` skips the registry check.
- An ApplicationSet in a pull request used to be ignored (exit 0 when it was the only change). It is now `NOT CHECKED` (exit 2). A Git path or a `$ref` values file in a different repository is `NOT CHECKED` (exit 2). Stageproof does not clone that repository. A missing values file is `NOT CHECKED` when `ignoreMissingValueFiles` is false. The same file used to be skipped, and the chart could render from its defaults.
- A Flux HelmRelease in a pull request used to be ignored (exit 0 when it was the only change). It is now checked. A GitRepository that is not a remote of this checkout is `NOT CHECKED` (exit 2). Stageproof does not clone that repository. A missing chart `valuesFiles` entry is `NOT CHECKED` (exit 2) when `ignoreMissingValuesFiles` is false. The chart is not rendered from its defaults. A deleted HelmRelease is `NOT CHECKED` (exit 2).
- An Argo CD Application check used to judge `crds/` as plain Helm, because the report adapter was always `helm`. The adapter is now `argocd`. When `skipCrds` is absent or false, an incompatible file is a warning (`crd.argocd_renders_crds`). When `skipCrds` is true, it is the Helm rule. A check that was `BLOCKED` only because the adapter was `helm` can now be `PASSED` when `skipCrds` is not true.
- An invalid enum value, such as a `restartPolicy` Kubernetes does not support, is `k8s.apply.invalid` and exits 1. The same object was `k8s.apply.unknown_field`, a warning with client-side Helm, and exited 0.
- A Helm hook is not reported as a delete or a restart. A hook PersistentVolumeClaim that the new chart drops was `impact.delete.pvc` (exit 1). It is not a finding now.
- CI behavior: when the current release does not become ready before the deadline and the upgrade of that same release does, a rollback that hits the same deadline is `NOT CHECKED` (exit 2). The same case was `BLOCKED` (exit 1).
- The default check starts a local API server. A Job pod template change and a StatefulSet `volumeClaimTemplates` change are `BLOCKED`. An unknown field is a warning with client-side Helm and `BLOCKED` when the apply is server-side (Helm 4, or `--server-side-apply`). `--api-server off` keeps the built-in table. `--api-server require` is `NOT CHECKED` (exit 2) when the binaries are not available. The result words are unchanged: an API server proof without `--live` is still `PASSED`.
- A chart that drops a templated CRD, a PersistentVolumeClaim, or a Namespace, that removes a CRD storage version, or that incompatibly changes a CRD under `crds/`, can now exit 1. The same input was exit 0, or exit 1 only when the API server rejected the object. `--api-server off` still classifies those cases from the rendered objects.

### Fixed

- The API server proof stores the current objects before it upgrades CRDs, then dry-runs an update of each stored custom resource against the new schema. A custom resource the new schema rejects is `crd.existing_object_invalid`. The report names the patch the process is running, and `fetch-apiserver` copies that process's binaries.
- CRD files under nested `crds/` directories and unpacked `charts/*/crds` are included. A schema break blocks only custom resources of that version that use the changed field. A newly required field the object already sets, and an enum value that is still allowed, stay warnings. Every affected custom resource is named.
- Removing a `securityContext` that set `runAsNonRoot`, `readOnlyRootFilesystem`, `allowPrivilegeEscalation: false`, or `capabilities.drop` is `impact.privilege.securitycontext`.
- API server details keep the constraint and drop the rejected value.
- The corpus workflow compares incident results and pair results with the committed baseline.
- `golang.org/x/net` is v0.60.0. The Kubernetes client libraries otherwise pull a module with the known HTTP/2 issues.
- The corpus label file parses again. One evidence line contained a colon, so no pair was labeled and the wrong-block count was zero for that reason. A labels file that does not parse now fails the run.

### Changed

- A pod from the previous Deployment revision is not an upgrade failure. The rehearsal judges the pods of the current revision.
- A missing image tag is printed in full. A rehearsal that stops because a CRD is not available stays `PASSED`, and `--require-rehearsal` still makes that `NOT CHECKED`.
- Release archives include an SBOM. The container image is signed by digest. A pre-release tag does not move `latest`.

## [0.1.8] - 2026-11-19

### Added

- The published image contains kind, kubectl, and the Docker client, so `--live` can run where the host can start containers.

## [0.1.7] - 2026-11-19

### Changed

- CI behavior: a saved live report no longer fails the job unless that report is `BLOCKED`. A file-only pass stays exit 0 and prints `NOT TESTED LIVE`.

## [0.1.6] - 2026-11-19

### Fixed

- A saved live report counts when its commit is on the merge request, not only when it is the head commit.

## [0.1.5] - 2026-11-19

### Changed

- CI behavior: a saved live report can fail the gate. A baseline that only hits the deadline is described as slow, and the upgrade uses that release.

### Fixed

- The operator signal test matches custom resources that the chart renders.

## [0.1.4] - 2026-11-19

### Changed

- CI behavior: a live run that does not come up exits 2. It is not a pass.

## [0.1.3] - 2026-11-19

### Added

- Image references that the chart prints into the render are named.

### Fixed

- The check reaches the new chart instead of stopping on the current one.

## [0.1.2] - 2026-11-19

### Added

- Image references that a render hides inside a value are named.

### Changed

- An empty rehearsal cluster is not described as the production cluster.

## [0.1.1] - 2026-11-19

### Added

- A green render still names a chart version jump, an image change, and a dependency that was not updated.

## [0.1.0] - 2026-11-17

### Added

- First release. `stageproof check` compares two Helm charts and reports `PROVEN`, `PASSED`, `BLOCKED`, or `NOT CHECKED`.
