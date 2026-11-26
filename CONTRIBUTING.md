# Contributing

Stageproof checks Helm chart, Argo CD Application, Flux HelmRelease, and rendered Kubernetes manifest upgrades. The scope is [docs/scope.md](docs/scope.md). A change that adds Terraform plans or image scanning does not belong in this repository.

## Build and test

```bash
go test ./...
go build -o stageproof ./cmd/stageproof
./stageproof version
```

Helm is required for the checks. The live test also needs kind, kubectl, and Docker, because it starts a real Kubernetes node. An unprivileged CI job cannot do that. The test stays off unless you set `STAGEPROOF_KIND_E2E=1`.

## Pull requests

- One change per pull request.
- If the result text, the exit codes, or the flags change, update `web/docs.html` from a real run in the same change.
- The `web/` pages and the chart index at `https://stageproof.github.io/index/` are one site: the same header, the same footer links, the same fonts, and the same `stageproof-theme` key. A change to the nav or the footer needs the same change in `internal/site/header.go` in `stageproof/index`, whose CI compares the two. `internal/website` checks that every page has the same nav and footer, that the home page code boxes hold only code (the explanation goes above the box), and that the fonts in `web/assets/fonts/` are self-hosted.
- Do not commit tokens, kubeconfigs, or chart values from a real cluster.

## Results

`PROVEN` and `PASSED` exit 0. `BLOCKED` exits 1. `NOT CHECKED` exits 2. Do not add a fourth exit code. What a version may change is in [docs/versioning.md](docs/versioning.md).

## Release checklist

1. `main` is green, including the `live` job and the corpus.
2. `CHANGELOG.md` has the new section. Anything under Breaking is also the first line of the release notes.
3. Version strings are updated in `cmd/stageproof/main.go`, `README.md`, `web/docs.html`, `web/index.html`, `web/security.html`, `web/architecture.html`, and `.github/ISSUE_TEMPLATE/bug.yml`. Check with `rg -n '0\.<old>\.<old>'`. It must return nothing.
4. That change is merged by pull request. Then `git tag vX.Y.Z <merge sha>` and `git push origin vX.Y.Z`. The tag is lightweight.
5. Watch the release run to the end. If one job fails for an outside reason, rerun that job once. If it fails twice, stop and report.
6. From a clean directory: download, `sha256sum -c`, `cosign verify-blob`, run `stageproof version`, and `cosign verify` the image.
7. Update the Homebrew formula by hand. Open the GitLab merge request that bumps the image pin.
8. Never move, delete, or rebuild a pushed tag. A bad release is fixed by the next version.
