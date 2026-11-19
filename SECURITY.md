# Security policy

## Reporting a vulnerability

Use GitHub private vulnerability reporting on [stageproof/stageproof](https://github.com/stageproof/stageproof): open the Security tab and choose "Report a vulnerability".

Do not open a public issue for a security report. There is no separate security email.

## What runs where

Stageproof runs on the machine that starts it. It does not phone home.

A file check runs `helm template`. Helm executes chart templates. Treat a chart as code.

`--live` starts a temporary kind cluster on that same machine, pulls the chart's images, and runs its hooks. It does not contact a cluster you already have. Run it on a machine you trust with that chart.

The node is a container that sets up its own networking and starts more containers. An unprivileged container is blocked from doing that, so a normal CI job cannot run `--live`.

## Supported versions

Security fixes apply to the latest released tag only.
