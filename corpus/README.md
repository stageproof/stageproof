# Accuracy corpus

This directory measures Stageproof. A case is a small chart pair, or a public chart version pair. The tool's own verdict is not the truth of a labeled pair.

## Add an incident

1. Create `incidents/<id>/` with `case.yaml`, `before/`, `after/`, and `control/`.
2. `before` is the current chart. `after` is the upgrade that contains the defect. `control` is the same upgrade without the defect.
3. `case.yaml` names the Kubernetes version, the verdict and rule ids, and `status`.

`status` is `active` when the check should already catch the defect. `expected-fail` means a later task (`needs`) will catch it, and that task has not shipped. `unreproduced` means the upstream report was read and the failure was not shown on a cluster. Do not invent a case from a title.

`go run ./cmd/corpus run --set incidents` must catch every `active` case and pass every control.

## Add a chart pair

`pairs/pairs.yaml` lists public charts: repository URL, chart name, `from`, and `to`. `pairs/pairs.lock` is the sha256 of each chart archive. The archives are downloaded at run time with `helm pull`. They are not committed.

A label in `labels/` is `{case, truth, evidence, by, date}`. `truth` is `clean` or `breaks`. The evidence is an upstream page or a `helm template` diff, not a Stageproof report. A same-major adjacent minor is labeled `clean`. A major boundary is labeled `breaks`.

`go run ./cmd/corpus compare results.json corpus/baseline.json` exits non-zero when an active incident is missed, a control is blocked, or a labeled-clean pair is blocked and was not blocked in the baseline. The corpus workflow runs that compare on the incident results and on the pair results.

`blocked_clean` is a labeled-clean pair that is `BLOCKED` after the tag was checked by hand. The label stays `clean`: a same-major minor is labeled `clean`. On 2026-11-21 the five entries are minor upgrades whose new image tag returned HTTP 404 `MANIFEST_UNKNOWN`. They are not rate limits and not 401 responses.

## Replay

`corpus/replay/cases.jsonl` is one JSON object per line: `repo`, `sha`, `path`, `from`, `to`. `repo` is `owner/name`. `sha` is the commit that contains the new chart version. `path` is the Flux HelmRelease file. `from` and `to` are the chart versions. The file does not store values, manifests, or secrets.

`go run ./cmd/corpus run --set replay --out replay.json` fetches each commit and its parent from GitHub, at most four at a time, and runs `stageproof check --flux-file` with `--api-server off`. The checkout is deleted after the case. The command prints how many cases were checked and the verdict counts. It does not print repository names. A check that exits 2 without a report is `NOT CHECKED`: the chart could not be read. A failed fetch is an error. `replay.json` is local output. Do not commit it.

The cases come from more than one public repository. Each HelmRelease's HelmRepository or OCIRepository is in that same repository, so the chart can be pulled. `from` and `to` are the chart versions in that commit.

`--set all` runs incidents and pairs. It does not run replay.

## Run

```bash
go run ./cmd/corpus run --set incidents --out results.json
go run ./cmd/corpus run --set pairs --out results.json
go run ./cmd/corpus compare results.json corpus/baseline.json
go run ./cmd/corpus run --set replay --out replay.json
```

`--set all` runs incidents and pairs. Replay is the separate command above. `replay/cases.jsonl` stores only a repository, a commit, a path, and two versions. It does not store values.
