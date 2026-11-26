# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Stageproof is not 1.0. The rules for what a version may change are in [docs/versioning.md](docs/versioning.md).

## [Unreleased]

## [0.5.3] - 2026-11-26

### Changed

- Markdown pull request and merge request comments (`--comment-file` and `--format markdown`) now use status icons, a compact metadata table, collapsible `<details>` sections for info-only restarts and non-blocking findings, and a clean findings table without empty `()` targets. Blockers still expand by default, and `NOT TESTED LIVE` stays directly under the heading.
- The Helm plugin default download is `v0.5.3`.

## [0.5.2] - 2026-11-26

### Fixed

- A password path inside nested subchart templates (`charts/<outer>/charts/<inner>/...`) is stored under each dependency. `'auth.password'` in `charts/artifactory/charts/postgresql` becomes `artifactory.postgresql.auth.password`. A Chart.yaml alias replaces the directory name when the chart files are present. `global.*` stays unprefixed. Camunda platform 8.3.24 to 8.3.25 and Artifactory OSS 107.111.10 to 107.111.11 stayed `PASSED`, and those pairs no longer fall back to install-mode rendering.
- The Helm plugin default download is `v0.5.2`.

## [0.5.1] - 2026-11-26

### Fixed

- A password path named inside a subchart template (`charts/<name>/...`) is stored under that dependency, so `'auth.replicationPassword'` in the postgresql subchart becomes `postgresql.auth.replicationPassword`. `--is-upgrade` can render those charts. Pairs that were already `PASSED` or `BLOCKED` stayed on that exit in the pairs checked for this release.
- The Helm plugin default download is `v0.5.1`.

## [0.5.0] - 2026-11-26

### Breaking

- A file check (`--api-server off`) now blocks a non-hook Job whose `spec.template` changed, including a `helm.sh/chart` label on the pod template. The same input previously returned `PASSED` (exit 0) and now returns `BLOCKED` (exit 1). A Helm hook is not compared. A template that uses `.Release.Revision` in the object name stays a warning. This matches the API server, which already blocked these Jobs.
- An upgrade that failed `helm template --is-upgrade` only because the chart asked for an existing password (`'path' must not be empty` or `--set path=`) now renders in upgrade mode with the same placeholder on both charts. If that render shows a blocker, the exit code changes from 0 to 1. Pairs that still cannot render in upgrade mode keep the `helm.template.upgrade_fallback` warning.

### Fixed

- Kubernetes rejecting `spec.strategy.rollingUpdate` because the strategy type is Recreate, or requiring `maxUnavailable` or `maxSurge` because the type is RollingUpdate, is explained as that conflict. The finding no longer says the field is immutable. Both directions are covered. The exit code is unchanged when the update was already blocked.
- The Helm plugin default download is `v0.5.0`.

## [0.4.0] - 2026-11-25

### Breaking

- Charts are now rendered with the target Kubernetes minor's served API versions and kinds (`group/version` and `group/version/Kind`) via `--api-versions`, taken from local API server discovery when it runs or from the embedded discovery table (`1.34` to `1.37`) when it does not. A removed-API finding whose template guards on `.Capabilities.APIVersions` for that legacy API version is now a warning, because `helm template` includes legacy API versions in its default list and cannot remove them. Charts that previously returned `BLOCKED` (exit 1) for capability-gated APIs now return `PASSED` (exit 0).
- When comparing an upgrade (`--current-chart` and `--new-chart`), the new chart is now rendered with `helm template --is-upgrade` while the current chart stays in install mode. Install-only Jobs under `.Release.IsInstall` and upgrade-only hook Jobs under `.Release.IsUpgrade` no longer trigger false `k8s.apply.immutable_field` blockers, which changes their exit code from 1 to 0. If `--is-upgrade` rendering fails and install-mode rendering succeeds, Stageproof falls back to install mode and emits a `helm.template.upgrade_fallback` warning.
- An immutable-field rejection (`k8s.apply.immutable_field`) on an object whose template uses `.Release.Revision` in a dynamic name is now a warning instead of a blocker, because `helm template` always renders revision 1 while `helm upgrade` creates a new name. Charts that previously returned `BLOCKED` (exit 1) for revision-named Jobs now return `PASSED` (exit 0).

### Added

- A Helm plugin downloads the released archive, checks `checksums.txt`, and runs `helm stageproof` with the same arguments and exit codes. The default binary is `v0.4.0`. Helm 4 installs it with `--verify=false` because a git checkout has no `.prov` file.
- Rule `helm.template.upgrade_fallback` (warning) records when the new chart failed `--is-upgrade` rendering and Stageproof fell back to install-mode rendering.

### Fixed

- Capability-gated templates using `.Capabilities.APIVersions.Has "<group>/<version>/<Kind>"` (such as `batch/v1/CronJob` and `policy/v1/PodDisruptionBudget`) now render the current API instead of falling back to removed beta APIs. Templates that guard legacy resources on `.Capabilities.APIVersions` no longer produce false `k8s.api.removed` blockers.
- Install-only Jobs gated by `.Release.IsInstall` and upgrade-only hook Jobs gated by `.Release.IsUpgrade` are now rendered accurately on the new chart instead of being compared as mutable non-hook Jobs.
- Objects named dynamically with `.Release.Revision` no longer trigger false `k8s.apply.immutable_field` blockers.

## [0.3.1] - 2026-11-24

### Fixed

- A custom resource definition written as `apiextensions.k8s.io/v1beta1` keeps the schema from `spec.validation` when a version does not set its own. A version with no schema no longer crashes the check. A version that already has a schema is compared as before.

## [0.3.0] - 2026-11-23

### Breaking

- A chart that does not render because a default is empty, a schema field is null, the schema document is invalid, or a template names a missing API is rendered again with a placeholder. The same placeholder is used on the current chart and the new chart. A value you set is not replaced. The report names the keys. A failure that appears only on the new chart stays a blocker. This changes the exit code for charts whose defaults do not render: the check can finish instead of returning `NOT CHECKED`.

### Added

- `stageproof comment` posts the Markdown file from `--comment-file` on a GitHub pull request or a GitLab merge request, and a later run updates the comment that contains `<!-- stageproof-comment -->`. `--provider auto` reads `GITHUB_ACTIONS` or `GITLAB_CI`. GitHub uses `GITHUB_TOKEN` and calls `api.github.com`, or `GITHUB_API_URL` when that is set. GitLab uses `STAGEPROOF_GITLAB_TOKEN`, `GITLAB_TOKEN`, or `CI_JOB_TOKEN`, and calls `CI_API_V4_URL` or `https://gitlab.com/api/v4`. A missing file, token, or permission exits 2. `--best-effort` exits 0 on HTTP 403. The token is not printed. The v0.2.0 binary does not include this command.
- `stageproof mcp` serves three read-only tools over stdin and stdout: `check`, `check_pr`, and `explain`. Paths must stay inside `--root`. It does not open a port, write a report file, or start a live rehearsal. `api_server` defaults to `off`. The v0.2.0 binary does not include this command.

### Security

- Chart-default render retries confine all template, schema, and values reads inside the chart directory, refuse symlinks escaping that directory, cap read size, and reject unsafe characters in generated `--set` arguments.
- Image checks use `github.com/google/go-containerregistry` v0.22.1. `github.com/docker/distribution` v2.8.3 is no longer in the module graph. `github.com/docker/cli` is v29.7.2.

### Fixed

- `stageproof check --repo` retries an `oci://` URL directly when it already ends with `/<chart>` and `<repo>/<chart>` fails.
- Render error scrubbing only redacts values passed by the caller, not boolean or placeholder values invented while retrying chart defaults.

## [0.2.0] - 2026-11-23

### Breaking

- `--saved-live` is removed. `check-pr` does not read `stageproof-live` or any other directory of saved rehearsal reports. A live rehearsal is `--live` on a runner that can start a temporary cluster.

### Added

- `stageproof check --repo` compares two versions of a chart in a Helm repository or an OCI registry. `--chart` is the chart name. `--from` and `--to` are the versions. The repository is `http://`, `https://`, or `oci://`. The report names each side as `chart@version`. The downloaded charts are removed when the command exits. A failed download is `NOT CHECKED` (exit 2). Without `--repo`, `--chart`, `--from`, and `--to` stay aliases for local chart directories. `--offline` does not skip the download. The v0.1.8 release does not have `--repo`.
- `.stageproof.yaml` accepts `smoke`. Each entry needs `name` and `run`. `timeout` is a Go duration greater than zero, and the default is `2m`. With `--live`, the commands run in the temporary cluster after the upgrade and before the rollback. A failure is `live.smoke_failed` and the release is rolled back. The output is scrubbed and capped. `STAGEPROOF_NAMESPACE` and `STAGEPROOF_RELEASE` are set, and `KUBECONFIG` is the temporary file. `check-pr` reads the list from the base ref. A `smoke` entry added only in the pull request does not run. Without `--live`, the commands do not run.
- `stageproof facts export` writes a JSON file from a kubeconfig and a context you pass. The calls are `get` and `list`. The default role is `deploy/facts-clusterrole.yaml` and it does not include Secrets. `--helm-releases` also needs `deploy/facts-helm-releases-clusterrole.yaml`. The file has the server version, node architectures, custom resource definitions (served versions, storage version, `storedVersions`, and the spec), validating admission policies and bindings, StorageClass names, and chart versions from Argo CD Applications and Flux HelmReleases. `--helm-releases` also reads Helm release Secrets and keeps the chart name, version, appVersion, release name, and namespace. Values are not written. `--cluster-facts` on `check` and `check-pr` uses that file. `--current-k8s` wins. Otherwise the server version wins over `.stageproof.yaml`. The custom resource definitions are installed on the local API server. Stored versions feed the CRD check. Each node architecture is a required image platform, and no nodes leaves that at `linux/amd64`. The file is not read from git. A missing command flag, a kubeconfig that cannot be read, or a forbidden request exits 2. The v0.1.8 release does not have this command.
- `.stageproof.yaml` accepts `ignores` and `policy`. An ignore needs `rule`, `reason`, and `expires` (`YYYY-MM-DD`; that date still matches). `resource` is a glob, and `*` matches every resource. The rule is the exact id. A match stays in the report, marked ignored with the reason and the date, and does not make the result `BLOCKED`. An expired entry does not match and adds `config.ignore_expired`. `policy` maps a rule id or a prefix to `block`, `warn`, or `info`. The longest match wins. `check-pr` reads the file from the base ref.
- `--format sarif` prints one SARIF 2.1.0 log. A blocker is `error`, a warning is `warning`, and info is `note`. The message is the finding message. Detail is not copied. `check-pr` writes one log for every chart. `--out` stays JSON. `docs/report.schema.json` is the shape of the JSON report, `schema_version` 1. Exit codes are unchanged.
- The terminal report and the Markdown comment list Deletes, CRDs, Privilege, Exposure, Values, Restarts, and Not checked before the check list. A section with nothing in it is left out. The lines are the finding messages and the reasons a check did not finish. The summary does not call a model. Exit codes are unchanged.
- `stageproof explain <rule-id>` prints the level, the one-line title, the meaning, and what to do for one rule. An unknown id exits 2. The findings tables in the docs are the same text. No rule id was renamed.
- `--helm-compare` renders the chart with the helm on `PATH` and with a second helm binary. Parsed objects are compared. A field that changes between two renders of the same binary is ignored. A remaining difference is `helm4.render_differs`, a warning. A binary that cannot render the chart is `helm4.render_failed` and exits 1. `Chart.yaml` `apiVersion: v3` is `helm4.chart_v3`, a warning. The commands this program runs were checked on Helm 3.22.1 and Helm 4.3.2.
- A new or changed container image tag is asked of the registry named in that reference. HTTP HEAD checks that the tag exists. A manifest list is fetched only to see whether `linux/amd64` is present. Layers are not downloaded. A missing tag (`supply.image.not_found`) or a missing platform (`supply.image.platform_missing`) is `BLOCKED`. A 401 or 403 is `supply.image.unverifiable` and does not block. A timeout, a rate limit, or a server error is `supply.registry_unreachable` and does not block. `supply.image.moved` names a repository change. `deprecated: true` in the new `Chart.yaml` is `supply.chart.deprecated`, a warning, including with `--offline`. `--offline` skips the registry and does not change `--api-server`. An unchanged image is not asked. This uses `github.com/google/go-containerregistry` v0.20.6. `github.com/klauspost/compress` is v1.20.2. A build of this commit is 61 MB.
- Values keys the template reader cannot decide are proven by rendering. Stageproof renders with the value, without it, and with another value of the same type when yours matches the chart default. A key with an effect in the current chart and none in the new chart stays `helm.values.silently_dropped`, now with `"proof": "render"`. A key read by an exact path stays `"proof": "templates"`. `--values-proof full` render-tests every key. The default is `default`. A YAML list is one key. Fields that change between two identical renders are ignored. A probe that `values.schema.json` rejects is not a finding. `helm.values.renamed` names a new key whose rendered effect matches, with `"verified": true`, and leaves a similar name unverified. `helm.values.default_changed` warns when a chart default you do not set still changes the render. The report names keys and chart defaults. It does not print your values. The check summary counts keys proven by rendering and keys read in templates.
- `stageproof check --before` and `--after` compare two rendered manifest files or directories. Helm is not called. Chart stages are recorded as skipped. The API server, the impact check, and the API table still run. `checked` is `rendered manifests`, or `rendered manifests, API server <patch>` when the API server ran. The report adapter is `manifests`. `--live`, values files, and `--set` with these flags exit 2. A path that cannot be read is `manifest.invalid_yaml`.
- An Argo CD Application check reports adapter `argocd`. A chart check stays `helm`. The Application's `skipCrds` is read. When the field is absent, `crds/` stays a warning that says so. `skipCrds: false` warns and says the CRDs are applied. `skipCrds: true` uses the plain Helm rule. `spec.sources`, `valueFiles`, `valuesObject`, `parameters`, `releaseName`, and a Git path in this checkout are read. `valuesObject` replaces `values`. A `$ref/` values file is read when that source is this repository. A missing values file is `NOT CHECKED` unless `ignoreMissingValueFiles` is true.
- A Flux HelmRelease check reports adapter `flux`. `helm.toolkit.fluxcd.io/v2`, `v2beta2`, and `v2beta1` are read. `spec.chart.spec` resolves a HelmRepository, an OCIRepository, or a GitRepository in this repository. `spec.chartRef` resolves an OCIRepository or a HelmChart in this repository. `spec.values` and `spec.valuesFrom` are applied in Flux order, and a `targetPath` entry overwrites earlier values. A ConfigMap or Secret that is a YAML file in the repository is a values file. A missing `valuesFrom` reference is `flux.values.unreadable`. The chart is still rendered and the result stays `PASSED`. `spec.upgrade.crds` is passed to the CRD check. When the field is omitted the policy is Skip, which is the helm-controller upgrade default. `check-pr` follows the HelmRelease, its source object, a `valuesFrom` file, and a chart path in this checkout.
- JSON reports start with `schema_version` 1. The field is added. Existing fields are unchanged.
- The accuracy corpus runs 12 active incident fixtures and 316 public CNCF Helm upgrade pairs across 105 CNCF projects, with labels backed by rendered diffs, CRD schema diffs, and registry checks. `go run ./cmd/corpus` is not in the release archives. `go run ./cmd/corpus run --set replay` checks the public Flux upgrades listed in `corpus/replay/cases.jsonl`. Each line is a repository, a commit, a path, and two chart versions. The cases come from more than one public repository, and the chart source for each HelmRelease is in that same repository. The command prints counts and timings.
- `--api-server auto` asks a local Kubernetes API server of the requested minor whether the upgrade is accepted. The binaries are `kube-apiserver`, `etcd`, and `kubectl` from the controller-tools envtest index, checked with the SHA-512 in that index, and cached on disk. `stageproof fetch-apiserver` downloads them for a machine that cannot fetch them later. The module now depends on `k8s.io` and `sigs.k8s.io`. A build of this commit is 57 MB.
- Rules `k8s.apply.immutable_field`, `k8s.apply.unknown_field`, `k8s.apply.invalid`, `crd.existing_object_invalid`, and `k8s.apply.not_exercised`.
- The report leads with what the upgrade deletes, restarts, exposes, or breaks. New rules: `impact.delete`, `impact.delete.crd`, `impact.delete.pvc`, `impact.delete.namespace`, `impact.delete.statefulset`, `impact.restart`, `impact.exposure.service`, `impact.exposure.route`, `impact.exposure.networkpolicy`, `impact.exposure.host`, `impact.privilege.rbac`, `impact.privilege.binding`, `impact.privilege.securitycontext`, `impact.privilege.hostpath`, `impact.privilege.webhook`, `impact.privilege.conversion`, `impact.capacity.replicas`, `impact.capacity.requests`, `impact.capacity.pdb`, `impact.capacity.hpa`, `crd.schema.incompatible`, `crd.scope_changed`, `crd.not_upgraded_by_helm`, `crd.argocd_renders_crds`, and `crd.flux_upgrade_crds`. `crd.existing_object_invalid` is also used when a storage version is removed or a custom resource uses an incompatible field. CRD schema comparison uses `sigs.k8s.io/crdify` v0.6.0. The module also depends on `k8s.io/apiextensions-apiserver`.
- When the API server did not run, the impact check says it compared the rendered objects. A restart is info. An object with `helm.sh/resource-policy: keep` is not reported as deleted.

### Changed

- The Helm release name for an Argo CD Application is `helm.releaseName`, or the Application name when that field is empty. Both revisions use that name. A chart check still uses `stageproof-rel`.

### Breaking

- `check-pr` reads `.stageproof.yaml` from `--current-git`, the base ref. A pull request that changes that file does not change the Kubernetes version, the values files, the ignores, or the policy used for the check, and the report includes `config.changed_in_pr`. The same pull request used to be checked with the file in the working directory. A `.stageproof.yaml` that is present and cannot be parsed now exits 2. It used to be skipped.
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

- API server upgrade checks no longer block when the current (`--current-chart`) chart has a pre-existing invalid manifest that the new chart fixes, and downgrade a pre-existing `k8s.apply.invalid` failure on the exact same fields in both charts to a warning. Validation causes are sorted so repeated runs report the same field first.
- Removing a CRD property's `enum` constraint completely, or removing an unreachable value from one `allOf` branch when another branch already forbade it, no longer reports an incompatible CRD schema change.
- A `required` value that sits inside an `if` or an `else` no longer blocks an upgrade that does not render that branch. An unconditional new `required` value still blocks.
- `GOOS=windows` builds cleanly again: Unix process-group termination for timed-out `smoke` commands is isolated behind `//go:build unix`.
- Container arguments and raw manifest scans no longer treat file paths (`/cert/tls.crt`) or untagged flag values (`--secret-type=kubernetes.io/tls`) as container images, preventing false `supply.image.not_found` blocks.
- Live rehearsal command errors and pod-log diagnostics scrub user values and redact `--set` / `--set-string` argument values.
- `check-pr --format json` and `--format sarif` send the "no charts modified" notice to stderr and keep stdout valid JSON or SARIF when no chart changed.
- `crd.schema.incompatible` deduplicates repeated field entries across CRD versions.
- A container env list is compared by name. Reordering env entries is not a change, and a value that changes between two renders masks that env entry only. A Flux Git chart in the working tree is copied before chart values files are written, so `values.yaml` in the checkout stays as it was. `--set` and Argo CD `helm.parameters` are proved from the values file. A dropped key stays `helm.values.silently_dropped`. A values file listed in `.stageproof.yaml` and missing on disk is `NOT CHECKED`. `check-pr` does not render that chart from chart defaults. Ignores and policy are applied before `--live`. An ignored blocker does not skip the rehearsal. A policy blocker does. The live line is appended to `checked`. An ignored delete, CRD finding, or packaged dependency is not a headline. A `policy` entry that makes `config.changed_in_pr` a blocker is the exit code. Helm errors scrub values from lists and from `--set`. An unquoted invalid API value is removed from the detail. Cluster facts preload strips a conversion webhook and does not report an unrelated install failure. A preloaded custom resource definition is the object an upgraded definition is dry-run against. `facts export` lists Helm Secrets with `type=helm.sh/release.v1` only. A build of this commit is 77 MB.
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
