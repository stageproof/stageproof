# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).
Stageproof is not 1.0. The rules for what a version may change are in [docs/versioning.md](docs/versioning.md).

## [Unreleased]

The next release is 0.2.0.

### Added

- JSON reports start with `schema_version` 1. The field is added. Existing fields are unchanged.

### Breaking

- CI behavior: when the current release does not become ready before the deadline and the upgrade of that same release does, a rollback that hits the same deadline is `NOT CHECKED` (exit 2). The same case was `BLOCKED` (exit 1).

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
