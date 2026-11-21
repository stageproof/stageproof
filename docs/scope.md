# What Stageproof does and does not do

Last updated: 2026-11-21.

This page is the scope of the command line tool.

## The job, in one paragraph

Stageproof looks at a pull request that changes a Helm chart and says whether that upgrade is safe to merge. The chart might live in the repository, an Argo CD Application might name the chart and the version, or a Flux HelmRelease might. Source on main can also compare two sets of rendered manifests. The released binary is v0.1.8 and does not read a HelmRelease or compare rendered manifests. The tool compares the version you run now with the version the pull request wants, using the values you actually set and the Kubernetes version you say the cluster runs. It prints one result and exits 0, 1, or 2. It does not edit the pull request, it does not merge it, and it does not change any cluster you already have.

## Two different upgrades

People say "Kubernetes update" for two different jobs. Stageproof only does the first one, and it uses the second as a fact you tell it.

| Upgrade | Example | What Stageproof does |
|---|---|---|
| Application upgrade. A Helm chart, the chart version an Argo CD Application points at, or the chart a Flux HelmRelease points at, moves to a newer release. | argo-cd chart 10.9.6 to 10.18.0. Redis image tag inside a chart. | This is the product. Stageproof compares the old release with the new one. |
| Cluster upgrade. The Kubernetes control plane and nodes move to a newer minor version. | The cluster moves from 1.35 to 1.36. | Stageproof does not upgrade a cluster, does not drain nodes, and does not check kubelet skew, PodDisruptionBudgets, or add-on compatibility for the platform itself. You pass `--current-k8s 1.35` and, if you want, `--next-k8s 1.36`. It then asks whether the chart you are upgrading still renders objects that those Kubernetes versions accept. |

`--current-k8s` is the version the cluster runs today. A chart that uses an API that version has already removed is `BLOCKED`. `--next-k8s` is the version you plan to move the cluster to later. An API that still works today but is removed in that next version is a warning, not a block. The pull request can merge. You have been told the cluster upgrade will break that object later.

`--live` does not rehearse a cluster upgrade. It starts a temporary empty kind cluster at the current Kubernetes version, installs the old chart, upgrades to the new chart, rolls back, and deletes that temporary cluster. The cluster you deploy to is never contacted.

`--live` starts a real Kubernetes node. That node is a container, and it has to set up its own networking and start more containers. An unprivileged container is blocked from doing those things, so kind has nothing to run the node with. A CI job that cannot start containers stays on the file check.

## Argo CD and Flux

Argo CD and Flux are both in the source on main. The released binary is v0.1.8. It reads one remote chart with inline `helm.values`. It does not read a HelmRelease.

Both tools are ways to write down "this Helm chart, this version, these values." The check behind them is the same chart upgrade. A Flux file is not a second product.

| Input | Now | Later, same check | Not the job |
|---|---|---|---|
| Helm chart in a directory, with your values files | Yes. `stageproof check --current-chart` and `--new-chart`. | | |
| Argo CD Application. One Helm chart, from a chart repository (`repoURL` and `chart`, including `oci://`) or from a directory in this checkout. Several sources, `valueFiles`, `valuesObject`, `parameters`, and `releaseName` are read. A `.yaml.tmpl` is accepted. `${...}` placeholders are replaced with fixed samples so the file parses. | Yes, in source on main. `stageproof check --argocd-file`. `check-pr` also follows a values file the Application names. An ApplicationSet, a chart or `$ref` values file in a different Git repository, a `ref` source that also sets `path`, and `sourceHydrator` are NOT CHECKED. Stageproof does not clone them and does not render chart defaults in their place. The released binary is v0.1.8 and reads one remote chart with inline `helm.values`. | | |
| Pull request or merge request | Yes. `stageproof check-pr` reads `git diff` and checks every changed chart, every changed Argo CD Application, and every changed Flux HelmRelease. A file deleted inside a chart is still a change to that chart. A source object, a `valuesFrom` file, or a file under a chart path the HelmRelease names is part of that check. It does not call Renovate, GitHub, or GitLab to ask what changed. | | |
| Flux `HelmRelease` | Yes, in source on main. `stageproof check --flux-file`. API versions `helm.toolkit.fluxcd.io/v2`, `v2beta2`, and `v2beta1`. `spec.chart.spec` resolves a HelmRepository, an OCIRepository, or a GitRepository in this repository. `spec.chartRef` resolves an OCIRepository or a HelmChart in this repository. `spec.values` and `spec.valuesFrom` are read. A missing `valuesFrom` object is a warning, the chart is still rendered, and the result stays `PASSED`. `spec.upgrade.crds` is read. Omitted means Skip. A GitRepository on another host, `ExternalArtifact`, a Bucket, both chart fields, and a missing chart `valuesFiles` entry are NOT CHECKED. Stageproof does not clone them and does not render chart defaults in their place. The released binary is v0.1.8 and does not read a HelmRelease. | | |
| Rendered YAML manifests | Yes, in source on main. `stageproof check --before` and `--after` read a `.yaml` or `.yml` file, or a directory of them. Chart stages are skipped. The API server, the impact check, and the API table run. `--live` with these flags is exit 2. `check-pr` still ignores a loose manifest that is not inside a chart. The v0.1.8 release does not have these flags. | | |
| Kustomize, Helmfile | No. The files are ignored. | | |
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
| Does the chart use an API your Kubernetes version has removed? | Yes | `--current-k8s` starts a local API server of that minor (`--api-server auto`). A kind the server does not serve is `BLOCKED`. If the binaries cannot be downloaded, the built-in table is used and the report says so. `--api-server off` uses only the table. `--api-server require` is `NOT CHECKED` when the binaries are missing. | The patch that ran is named in `checked:`. It is not a claim that your cluster is that patch. Deprecations still come from the table. |
| Will the next Kubernetes version remove an API the chart still uses? | Yes, as a warning | `--next-k8s` dry-runs the new objects as creates on a second local API server when that minor is published. Otherwise the table is used. | It does not block the chart upgrade. It does not upgrade the cluster. |
| Can the upgrade be applied on top of the old release? | Partly | The local API server applies the current objects and dry-runs the new ones. An immutable field, including a Job pod template and a StatefulSet `volumeClaimTemplates` change, is `BLOCKED`. A label selector change is still `BLOCKED`. A Helm hook is dry-run as a create. An unknown field is a warning with client-side Helm and `BLOCKED` with server-side apply. An unsupported enum value is `BLOCKED`. A Helm hook is not reported as a delete or a restart. CRDs larger than 256 KiB are a warning. The same run compares the two object sets for deletes, restarts, exposure, privilege, capacity, and CRD schema changes. When the API server did not run, that check says the comparison used the rendered objects. | A templated CRD, a PersistentVolumeClaim, or a Namespace that the new chart drops is `BLOCKED`, unless the object has `helm.sh/resource-policy: keep`. A StatefulSet delete is `BLOCKED` only when `persistentVolumeClaimRetentionPolicy.whenDeleted` is `Delete`. A CRD scope change, a removed storage version, or a field change that an existing custom resource of that version uses is `BLOCKED`. A newly required field the object already sets, and an enum value that is still allowed, are warnings. Other schema changes are a warning. An incompatible change to a CRD under `crds/` is `BLOCKED`, because plain Helm does not upgrade that directory. A Flux HelmRelease uses `spec.upgrade.crds`. Omitted and Skip are that same block. Create warns. CreateReplace is info. The helm-controller type comment says the upgrade default is Skip. The user guide lists Create as the default for both install and upgrade. This check follows the type comment. A restart, a lower replica count, and a higher resource request are info. The report does not print values. An API server proof without `--live` is still `PASSED`. |
| Does the new release start, and can you roll it back? | Only if you pass `--live` | Temporary kind cluster on the machine running the command. Each step waits up to 10 minutes. Install, upgrade, roll back, delete. If the current chart misses the deadline and the release is still there, the next step upgrades that release with the same values. A fresh install of the new chart is only used when there is no release to upgrade, and it is not called an upgrade. The deadline is reported as `context deadline exceeded`. A call to the cluster's own API is not an outside service. | Your real data, traffic, secrets, and private images. A slow empty-cluster install is not a broken chart. |

## What a result means

| Result | Exit | Human reading |
|---|---|---|
| `PROVEN` | 0 | The files passed, and the temporary cluster installed the old chart, upgraded, and rolled back. |
| `PASSED` | 0 | The files passed. When no rehearsal ran, the job log prints `NOT TESTED LIVE` in red and the comment repeats that sentence. Exit 0 means the pipeline is not blocked. It does not mean the upgrade was rehearsed. |
| `BLOCKED` | 1 | The upgrade has a problem the report names. CI should fail. |
| `NOT CHECKED` | 2 | Stageproof did not finish, or `--require-rehearsal` was set and no upgrade rehearsal passed. CI should fail. This does not mean the upgrade is unsafe. A file check that did not rehearse stays `PASSED` and says it was not tested live. |

`check-pr` reads `stageproof-live/**/report.json` when that file is already in the checkout (`--saved-live` changes the directory). The file counts when its `head_git` is a commit on the merge request. The comment includes that rehearsal. A rehearsal that could not test the upgrade does not fail the job. A pipeline that runs before the file exists cannot see it, and its `PASSED` line says the upgrade was not tested live.
