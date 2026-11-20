# Two images from one file.
# The default target, live, can start a temporary cluster.
# The stageproof target is the smaller image: Stageproof, Helm, and Git.
FROM golang:1.27-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/stageproof ./cmd/stageproof

FROM debian:bookworm-slim AS stageproof
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git \
 && case "${TARGETARCH}" in \
      amd64) helm_sha=896cf63684a42a723d61c980369d6e588330927deb13676ca0d85006dd6da0ea ;; \
      arm64) helm_sha=4c29e286b279d170fa048ddfc3e4e83dcd069ca9c52102766ba672d7149e694d ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v3.22.1-linux-${TARGETARCH}.tar.gz" \
 && echo "${helm_sha}  /tmp/helm.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helm.tgz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && chmod 755 /usr/local/bin/helm \
 && rm -rf /var/lib/apt/lists/* /tmp/helm.tgz /tmp/linux-* \
 && git config --system --add safe.directory '*' \
 && useradd --create-home --uid 65532 stageproof \
 && helm version --short
COPY --from=build /out/stageproof /usr/local/bin/stageproof
USER stageproof
WORKDIR /work
ENTRYPOINT ["stageproof"]

# kind, kubectl, and the Docker client. --live still needs the host Docker daemon.
# Docker's static archive has no published checksum file. The hashes in the
# next step are the hashes of those URLs, pinned so a changed file fails the build.
FROM stageproof AS live
USER root
ARG TARGETARCH
ARG KIND_VERSION=0.33.0
ARG KUBECTL_VERSION=1.35.10
ARG DOCKER_VERSION=29.9.2
RUN case "${TARGETARCH}" in \
      amd64) docker_arch=x86_64; kind_sha=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d; kubectl_sha=c49a28f1f4efc4d475ed05f47f2ce8e563bf7bef69ed77fe1b924ea6d83b6327; docker_sha=9ac225999fd5e863f7694c728ef23d480616b1e5639328fa309befd4243d69eb ;; \
      arm64) docker_arch=aarch64; kind_sha=20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2; kubectl_sha=283f1be6a46b3695b625e25030106b8ffe18bd0d3a900f5b65c259ca407f04f5; docker_sha=873b3e0207857c3b2f28392fb922f3ea6efe37620c16d4d4aaf2fef409c90b73 ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o "/tmp/kind" "https://github.com/kubernetes-sigs/kind/releases/download/v${KIND_VERSION}/kind-linux-${TARGETARCH}" \
 && echo "${kind_sha}  /tmp/kind" | sha256sum -c - \
 && curl -fsSL -o /tmp/kubectl "https://dl.k8s.io/release/v${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
 && echo "${kubectl_sha}  /tmp/kubectl" | sha256sum -c - \
 && curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/${docker_arch}/docker-${DOCKER_VERSION}.tgz" \
 && echo "${docker_sha}  /tmp/docker.tgz" | sha256sum -c - \
 && tar -xzf /tmp/docker.tgz -C /tmp docker/docker \
 && mv /tmp/kind /usr/local/bin/kind \
 && mv /tmp/kubectl /usr/local/bin/kubectl \
 && mv /tmp/docker/docker /usr/local/bin/docker \
 && chmod 755 /usr/local/bin/kind /usr/local/bin/kubectl /usr/local/bin/docker \
 && rm -rf /tmp/docker.tgz /tmp/docker \
 && kind version \
 && kubectl version --client=true \
 && docker --version
USER stageproof
