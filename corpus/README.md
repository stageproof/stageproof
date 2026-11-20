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

`go run ./cmd/corpus compare results.json corpus/baseline.json` exits non-zero when an active incident is missed, a control is blocked, or a labeled-clean pair is blocked and was not blocked in the baseline.

## Run

```bash
go run ./cmd/corpus run --set incidents --out results.json
go run ./cmd/corpus run --set pairs --out results.json
go run ./cmd/corpus compare results.json corpus/baseline.json
```

`--set all` runs incidents and pairs. Replay waits for the Flux adapter. The file `replay/cases.jsonl` stays empty until then, and it stores only a repository URL, a commit, a path, and two versions. It does not store values.
