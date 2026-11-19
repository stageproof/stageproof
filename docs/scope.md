# What Stageproof does and does not do

Last updated: 2026-11-17.

This page is the scope of the command line tool.

## The job, in one paragraph

Stageproof looks at a pull request that changes a Helm chart and says whether that upgrade is safe to merge. The chart might live in the repository, or an Argo CD Application might name the chart and the version. The tool compares the version you run now with the version the pull request wants, using the values you actually set and the Kubernetes version you say the cluster runs. It prints one result and exits 0, 1, or 2. It does not edit the pull request, it does not merge it, and it does not change any cluster you already have.

## Two different upgrades

People say "Kubernetes update" for two different jobs. Stageproof only does the first one, and it uses the second as a fact you tell it.

| Upgrade | Example | What Stageproof does |
|---|---|---|
| Application upgrade. A Helm chart, or the chart version an Argo CD Application points at, moves to a newer release. | argo-cd chart 10.9.6 to 10.18.0. Redis image tag inside a chart. | This is the product. Stageproof compares the old release with the new one. |
| Cluster upgrade. The Kubernetes control plane and nodes move to a newer minor version. | The cluster moves from 1.35 to 1.36. | Stageproof does not upgrade a cluster, does not drain nodes, and does not check kubelet skew, PodDisruptionBudgets, or add-on compatibility for the platform itself. You pass `--current-k8s 1.35` and, if you want, `--next-k8s 1.36`. It then asks whether the chart you are upgrading still renders objects that those Kubernetes versions accept. |

`--current-k8s` is the version the cluster runs today. A chart that uses an API that version has already removed is `BLOCKED`. `--next-k8s` is the version you plan to move the cluster to later. An API that still works today but is removed in that next version is a warning, not a block. The pull request can merge. You have been told the cluster upgrade will break that object later.

`--live` does not rehearse a cluster upgrade. It starts a temporary empty kind cluster at the current Kubernetes version, installs the old chart, upgrades to the new chart, rolls back, and deletes that temporary cluster. The cluster you deploy to is never contacted.

## Argo CD and Flux

Argo CD is in the product now. Flux is not built. Flux was not rejected.

Both tools are ways to write down "this Helm chart, this version, these values." The check behind them is the same chart upgrade. We read Argo CD first because that is the file this repository uses and the file most GitOps clusters use. A Flux `HelmRelease` is a later input for that same check. It is on the build list. It is not a second product, and a Flux file in a pull request is ignored today.

| Input | Now | Later, same check | Not the job |
|---|---|---|---|
| Helm chart in a directory, with your values files | Yes. `stageproof check --current-chart` and `--new-chart`. | | |
| Argo CD Application that names one Helm chart (HTTPS repo or `oci://`) and carries inline `helm.values` | Yes. `stageproof check --argocd-file`. A `.yaml.tmpl` is accepted. `${...}` placeholders are replaced with fixed samples so the file parses. | Applications with several sources, `valueFiles`, `valuesObject`, or a Git directory as the source. Those are not read today. | |
| Pull request or merge request | Yes. `stageproof check-pr` reads `git diff` and checks every changed chart and every changed Argo CD Application. It does not call Renovate, GitHub, or GitLab to ask what changed. | | |
| Flux `HelmRelease` | No. The file is ignored. | Yes. Same chart check, once the Flux adapter exists. | |
| Kustomize, Helmfile, plain YAML manifests | No. The files are ignored. | Yes. Any two rendered manifest sets, so the check does not care which tool rendered them. | |
| Terraform providers, modules, and plans | No. | | A provider or module version change is not a Helm upgrade. |
| Container image contents (base image, libc, user, entrypoint) | No. An image tag inside a chart is just a string in the render. | A supply check that the tag exists and the registry is reachable. Not an ABI or CVE scanner. | Deciding whether a new base image is safe to run. |
| Language packages (npm, Maven, Go modules, and the rest) | No. | No. | Renovate already has Merge Confidence for those. |
| Kubernetes cluster upgrade itself | No. See the table above. We only use the version number you give us while judging a chart. | A read-only export of what a real cluster has installed, so the version number is not typed by hand. Still not us performing the upgrade. | Drains, skew, control-plane add-ons, and "click upgrade" on EKS, GKE, or AKS. |

## What the check looks at today

| Question | Answered now? | How | What it does not prove |
|---|---|---|---|
| Does the new chart render with your values? | Yes | `helm template` | That the rendered objects are the ones you wanted. |
| Does every key in your values file still do something? | Partly | It reads the chart templates and records which `.Values` keys they mention. A key the old chart reads and the new chart does not read is `BLOCKED`. Argo CD `helm.parameters` and `--set` are included as a values file. | A key the template mentions and then ignores still looks used. Rendering with and without the key is planned. |
| Did the chart version, appVersion, image ref, or a Chart.yaml dependency change? | Yes, as a warning | The report names a minor-version skip, a new major, a changed `appVersion`, a changed container image, and a dependency constraint whose packaged chart under `charts/` did not change. A tag that only gains or loses a leading `v` is called out. | That the upstream project forbids the skip, or that the registry tag exists. |
| Does the chart use an API your Kubernetes version has removed? | Yes, from a table we maintain | `--current-k8s` | This is not a live API server. Unknown fields and CRD validation rules are not judged. A real API server is planned. |
| Will the next Kubernetes version remove an API the chart still uses? | Yes, as a warning | `--next-k8s` | It does not block the chart upgrade. It does not upgrade the cluster. |
| Can the upgrade be applied on top of the old release? | Partly | Label selectors that Kubernetes refuses to change are `BLOCKED`. CRDs larger than 256 KiB are a warning, because client-side apply will fail. | Deletes, restarts, new privileges, and CRD schema breaks are not classified yet. |
| Does the new release start, and can you roll it back? | Only if you pass `--live` | Temporary kind cluster on the machine running the command. Each step waits up to 10 minutes. Install, upgrade, roll back, delete. If the current chart does not become ready, the new chart is installed on its own and the result says that was not an upgrade. A call to the cluster's own API is not an outside service. | Your real data, traffic, secrets, and private images. An empty cluster that cannot start the old chart is not a failed upgrade. |

## What a result means

| Result | Exit | Human reading |
|---|---|---|
| `PROVEN` | 0 | The files passed, and the temporary cluster installed the old chart, upgraded, and rolled back. |
| `PASSED` | 0 | The files passed. No cluster was used, or the rehearsal stopped because of something outside the chart (missing CRD, private image, missing Secret). The report says `checked: files`. |
| `BLOCKED` | 1 | The upgrade has a problem the report names. CI should fail. |
| `NOT CHECKED` | 2 | Stageproof did not finish, or `--require-rehearsal` was set and no rehearsal passed. CI should fail. This does not mean the upgrade is unsafe. |
