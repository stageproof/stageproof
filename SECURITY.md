# Security policy

## Reporting a vulnerability

Use GitHub private vulnerability reporting on [stageproof/stageproof](https://github.com/stageproof/stageproof): open the Security tab and choose "Report a vulnerability".

Do not open a public issue for a security report. There is no separate security email.

## What runs where

Stageproof runs on the machine that starts it. It does not phone home.

A file check runs `helm template`. Helm executes chart templates. Treat a chart as code. A check of an Argo CD Application or a Flux HelmRelease also runs `helm pull` when the chart is a Helm repository or an OCI artifact named in that file. It reads a chart path in this checkout from Git. It does not clone another Git repository. Secret values from a `valuesFrom` Secret are not printed.

With `--api-server auto` (the default) or `require`, the check downloads `kube-apiserver`, `etcd`, and `kubectl` for the Kubernetes minor you named, unless `--api-server-dir` already points at them. The index is `https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/HEAD/envtest-releases.yaml`. The archives are release assets of `kubernetes-sigs/controller-tools` on `github.com`. The SHA-512 in the index is checked before the archive is unpacked. The files are cached under the user cache directory, in `stageproof/envtest`. The processes listen on localhost and are stopped when the check exits. They do not contact a cluster you already have. `--api-server off` does not download them and does not start them.

`--live` starts a temporary kind cluster on that same machine, pulls the chart's images, and runs its hooks. It does not contact a cluster you already have. Run it on a machine you trust with that chart.

The node is a container that sets up its own networking and starts more containers. An unprivileged container is blocked from doing that, so a normal CI job cannot run `--live`.

## Supported versions

Security fixes apply to the latest released tag only.
