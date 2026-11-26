# Versioning

Stageproof is not 1.0.

There are four results and three exit codes. `PROVEN` and `PASSED` exit 0. `BLOCKED` exits 1. `NOT CHECKED` exits 2. A release does not add a result or an exit code.

Before 1.0, a minor version (`0.x.0`) may change behavior. The changelog lists that change under Breaking. A patch version never changes an exit code for the same input.

Rule IDs and JSON fields are only added. A released ID is not renamed and not reused.

The current release is 0.5.0.
