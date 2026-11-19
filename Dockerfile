# Stageproof, Helm, Git, kind, kubectl, and the Docker client.
# --live uses the Docker daemon on the machine. Mount its socket,
# use host networking, and run as root. See the README.
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stageproof ./cmd/stageproof

FROM debian:bookworm-slim
ARG TARGETARCH
ARG KIND_VERSION=0.27.0
ARG KUBECTL_VERSION=1.35.0
ARG DOCKER_VERSION=29.9.2
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git \
 && curl -fsSL "https://get.helm.sh/helm-v3.17.1-linux-${TARGETARCH}.tar.gz" | tar -xz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && case "${TARGETARCH}" in \
      amd64) docker_arch=x86_64 ;; \
      arm64) docker_arch=aarch64 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o "/tmp/kind-linux-${TARGETARCH}" "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${TARGETARCH}" \
 && curl -fsSL -o /tmp/kind.sha256 "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${TARGETARCH}.sha256sum" \
 && (cd /tmp && sha256sum -c kind.sha256) \
 && curl -fsSL -o /tmp/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
 && echo "$(curl -fsSL "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl.sha256")  /tmp/kubectl" | sha256sum -c - \
 && curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/${docker_arch}/docker-${DOCKER_VERSION}.tgz" \
 && tar -xzf /tmp/docker.tgz -C /tmp docker/docker \
 && mv "/tmp/kind-linux-${TARGETARCH}" /usr/local/bin/kind \
 && mv /tmp/kubectl /usr/local/bin/kubectl \
 && mv /tmp/docker/docker /usr/local/bin/docker \
 && chmod 755 /usr/local/bin/helm /usr/local/bin/kind /usr/local/bin/kubectl /usr/local/bin/docker \
 && rm -rf /var/lib/apt/lists/* /tmp/linux-* /tmp/kind.sha256 /tmp/docker.tgz /tmp/docker \
 && git config --system --add safe.directory '*' \
 && useradd --create-home --uid 65532 stageproof \
 && kind version \
 && kubectl version --client=true \
 && docker --version \
 && helm version --short
COPY --from=build /out/stageproof /usr/local/bin/stageproof
USER stageproof
WORKDIR /work
ENTRYPOINT ["stageproof"]
