# Accuracy corpus

This directory measures Stageproof. A case is a small chart pair, or a public chart version pair. The tool's own verdict is not the truth of a labeled pair.

## Add an incident

1. Create `incidents/<id>/` with `case.yaml`, `before/`, `after/`, and `control/`.
2. `before` is the current chart. `after` is the upgrade that contains the defect. `control` is the same upgrade without the defect.
3. `case.yaml` names the Kubernetes version, the verdict and rule ids, and `status`.

`status` is `active` when the check should already catch the defect. `expected-fail` means a later task (`needs`) will catch it, and that task has not shipped. `unreproduced` means the upstream report was read and the failure was not shown on a cluster. Do not invent a case from a title.

`go run ./cmd/corpus run --set incidents` must catch every `active` case and pass every control.

## Add a chart pair

`pairs/pairs.yaml` lists public CNCF Helm charts: repository URL (HTTP or OCI), chart name, `from`, and `to`. `pairs/pairs.lock` is the sha256 of each chart archive. The archives are downloaded at run time with `helm pull`. They are not committed.

A label in `labels/chart-pairs.yaml` is `{case, truth, evidence, by, date}`. `truth` is `clean` or `breaks`. The evidence comes from an independent `helm template` diff (`rendered-diff`), a CRD file OpenAPI diff (`chart-file`), or a registry HEAD check (`registry-check`), not a Stageproof report. Labels are not inferred from version boundaries.

`go run ./cmd/corpus compare results.json corpus/baseline.json` exits non-zero when an active incident is missed, a control is blocked, or a labeled-clean pair is blocked and was not blocked in the baseline. The corpus workflow runs that compare on `incidents.json` and on the pair results (both `--api-server off` and `--api-server auto` on full runs, and `--pr` on pull requests).

`blocked_clean` is empty on the current 316-pair CNCF corpus (105 CNCF projects, 281 labeled clean, 35 labeled breaks). Five retired archived Bitnami/Airflow pairs and two unreproduced incident fixtures (`conversion-webhook-breaks-api` and `keep-annotated-crd-migration`, whose fixtures never produced their named rules) were removed.

## Replay

`corpus/replay/cases.jsonl` is one JSON object per line: `repo`, `sha`, `path`, `from`, `to`. `repo` is `owner/name`. `sha` is the commit that contains the new chart version. `path` is the Flux HelmRelease file. `from` and `to` are the chart versions. The file does not store values, manifests, or secrets.

`go run ./cmd/corpus run --set replay --out replay.json` fetches each commit and its parent from GitHub, at most four at a time, and runs `stageproof check --flux-file` with `--api-server off`. The checkout is deleted after the case. The command prints how many cases were checked and the verdict counts. It does not print repository names. A check that exits 2 without a report is `NOT CHECKED`: the chart could not be read. A failed fetch is an error. `replay.json` is local output. Do not commit it.

The cases come from more than one public repository. Each HelmRelease's HelmRepository or OCIRepository is in that same repository, so the chart can be pulled. `from` and `to` are the chart versions in that commit.

`--set all` runs incidents and pairs. It does not run replay.

## Run

```bash
go run ./cmd/corpus run --set incidents --out results.json
go run ./cmd/corpus run --set pairs --out results.json
go run ./cmd/corpus run --set pairs --api-server off --out pairs-files.json
go run ./cmd/corpus compare results.json corpus/baseline.json
go run ./cmd/corpus run --set replay --out replay.json
```

`--set all` runs incidents and pairs. Replay is the separate command above. `replay/cases.jsonl` stores only a repository, a commit, a path, and two versions. It does not store values.
