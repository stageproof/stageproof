# Contributing

Stageproof checks a Helm chart upgrade. The scope is [docs/scope.md](docs/scope.md). A change that adds Terraform plans or image scanning does not belong in this repository.

## Build and test

```bash
go test ./...
go build -o stageproof ./cmd/stageproof
./stageproof version
```

Helm is required for the checks. The live rehearsal also needs kind, kubectl, and Docker. That test stays off unless you set `STAGEPROOF_KIND_E2E=1`.

## Pull requests

- One change per pull request.
- If the result text, the exit codes, or the flags change, update `web/docs.html` from a real run in the same change.
- Do not commit tokens, kubeconfigs, or chart values from a real cluster.

## Results

`PROVEN` and `PASSED` exit 0. `BLOCKED` exits 1. `NOT CHECKED` exits 2. Do not add a fourth exit code.
