# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Stageproof is not 1.0. The rules for what a version may change are in [docs/versioning.md](docs/versioning.md).

## [Unreleased]

The next release is 0.2.0.

### Added

- JSON reports start with `schema_version` 1. The field is added. Existing fields are unchanged.
- The accuracy corpus runs the incident fixtures and 100 public chart pairs. `go run ./cmd/corpus` is not in the release archives.
- `--api-server auto` asks a local Kubernetes API server of the requested minor whether the upgrade is accepted. The binaries are `kube-apiserver`, `etcd`, and `kubectl` from the controller-tools envtest index, checked with the SHA-512 in that index, and cached on disk. `stageproof fetch-apiserver` downloads them for a machine that cannot fetch them later. The module now depends on `k8s.io` and `sigs.k8s.io`. A build of this commit is 57 MB.
- Rules `k8s.apply.immutable_field`, `k8s.apply.unknown_field`, `k8s.apply.invalid`, `crd.existing_object_invalid`, and `k8s.apply.not_exercised`.
- The report leads with what the upgrade deletes, restarts, exposes, or breaks. New rules: `impact.delete`, `impact.delete.crd`, `impact.delete.pvc`, `impact.delete.namespace`, `impact.delete.statefulset`, `impact.restart`, `impact.exposure.service`, `impact.exposure.route`, `impact.exposure.networkpolicy`, `impact.exposure.host`, `impact.privilege.rbac`, `impact.privilege.binding`, `impact.privilege.securitycontext`, `impact.privilege.hostpath`, `impact.privilege.webhook`, `impact.privilege.conversion`, `impact.capacity.replicas`, `impact.capacity.requests`, `impact.capacity.pdb`, `impact.capacity.hpa`, `crd.schema.incompatible`, `crd.scope_changed`, `crd.not_upgraded_by_helm`, `crd.argocd_renders_crds`, and `crd.flux_upgrade_crds`. `crd.existing_object_invalid` is also used when a storage version is removed or a custom resource uses an incompatible field. CRD schema comparison uses `sigs.k8s.io/crdify` v0.6.0. The module also depends on `k8s.io/apiextensions-apiserver`.
- When the API server did not run, the impact check says it compared the rendered objects. A restart is info. An object with `helm.sh/resource-policy: keep` is not reported as deleted.

### Breaking

- CI behavior: when the current release does not become ready before the deadline and the upgrade of that same release does, a rollback that hits the same deadline is `NOT CHECKED` (exit 2). The same case was `BLOCKED` (exit 1).
- The default check starts a local API server. A Job pod template change and a StatefulSet `volumeClaimTemplates` change are `BLOCKED`. An unknown field is a warning with client-side Helm and `BLOCKED` when the apply is server-side (Helm 4, or `--server-side-apply`). `--api-server off` keeps the built-in table. `--api-server require` is `NOT CHECKED` (exit 2) when the binaries are not available. The result words are unchanged: an API server proof without `--live` is still `PASSED`.
- A chart that drops a templated CRD, a PersistentVolumeClaim, or a Namespace, that removes a CRD storage version, or that incompatibly changes a CRD under `crds/`, can now exit 1. The same input was exit 0, or exit 1 only when the API server rejected the object. `--api-server off` still classifies those cases from the rendered objects.

### Fixed

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
