# Changelog

## Unreleased

### Changed

- CI behavior: when the current release does not become ready before the deadline and the upgrade of that same release does, a rollback that hits the same deadline stays `NOT CHECKED`. It is not `BLOCKED`, and it is not `PROVEN`.
- A pod from the previous Deployment revision is not an upgrade failure. The rehearsal judges the pods of the current revision.
- A missing image tag is printed in full. A rehearsal that stops because a CRD is not available stays `PASSED`, and `--require-rehearsal` still makes that `NOT CHECKED`.
