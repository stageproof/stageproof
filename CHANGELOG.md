# Changelog

## Unreleased

### Changed

- CI behavior: when the current release does not become ready before the deadline and the upgrade of that same release does, a rollback that hits the same deadline stays `NOT CHECKED`. It is not `BLOCKED`, and it is not `PROVEN`.
