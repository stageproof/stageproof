# Security policy

## Reporting a vulnerability

Use GitHub private vulnerability reporting on [stageproof/stageproof](https://github.com/stageproof/stageproof): open the Security tab and choose "Report a vulnerability".

Do not open a public issue for a security report. There is no separate security email.

## What runs where

Stageproof runs on the machine that starts it. It does not phone home. `--helm-compare` runs a second helm binary on that machine. It does not add a network call.

A file check runs `helm template`. Helm executes chart templates. Treat a chart as code. A check of an Argo CD Application or a Flux HelmRelease also runs `helm pull` when the chart is a Helm repository or an OCI artifact named in that file. It reads a chart path in this checkout from Git. It does not clone another Git repository. Secret values from a `valuesFrom` Secret are not printed.

The corpus replay command is not in the release binary. It fetches the public commits named in `corpus/replay/cases.jsonl` from `github.com`, at most four at a time, and deletes each checkout when the case finishes. Its printed summary is counts and timings.

With `--api-server auto` (the default) or `require`, the check downloads `kube-apiserver`, `etcd`, and `kubectl` for the Kubernetes minor you named, unless `--api-server-dir` already points at them. The index is `https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/HEAD/envtest-releases.yaml`. The archives are release assets of `kubernetes-sigs/controller-tools` on `github.com`. The SHA-512 in the index is checked before the archive is unpacked. The files are cached under the user cache directory, in `stageproof/envtest`. The processes listen on localhost and are stopped when the check exits. They do not contact a cluster you already have. `--api-server off` does not download them and does not start them.

A new or changed container image is asked of the registry named in that image reference. The request is HTTP HEAD. A manifest list is fetched only to see whether `linux/amd64` is present, or each node architecture from `--cluster-facts` when that file lists nodes. Image layers are not downloaded. The host is whatever the chart rendered. There is no fixed list of registries. `--offline` sends none of those requests and does not change `--api-server`. An unchanged image is not asked. A 401 or 403 is reported as unverifiable and is not a block.

`stageproof facts export` reads the cluster named by `--kubeconfig` and `--context`. It does not open the default kubeconfig. The calls are get and list. The default role is `deploy/facts-clusterrole.yaml`. It does not include Secrets. Helm release Secrets are read only with `--helm-releases`, which also needs `deploy/facts-helm-releases-clusterrole.yaml`. Only the chart name, version, appVersion, release name, and namespace are written. Values are not. The Secret list is limited to type `helm.sh/release.v1`. `--cluster-facts` reads that file from the path you pass. It is not read from git. The host is the one in the kubeconfig. There is no fixed cluster address.

`--live` starts a temporary kind cluster on that same machine, pulls the chart's images, and runs its hooks. It does not contact a cluster you already have. Run it on a machine you trust with that chart.

The node is a container that sets up its own networking and starts more containers. An unprivileged container is blocked from doing that, so a normal CI job cannot run `--live`.

## Supported versions

Security fixes apply to the latest released tag only.
