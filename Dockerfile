# Image for file checks. Helm and Git are included.
# --live is not supported here: it needs a Docker daemon on the host.
FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stageproof ./cmd/stageproof

FROM debian:bookworm-slim
ARG TARGETARCH
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl git \
 && curl -fsSL "https://get.helm.sh/helm-v3.17.1-linux-${TARGETARCH}.tar.gz" | tar -xz -C /tmp \
 && mv "/tmp/linux-${TARGETARCH}/helm" /usr/local/bin/helm \
 && rm -rf /var/lib/apt/lists/* /tmp/linux-* \
 && useradd --create-home --uid 65532 stageproof
COPY --from=build /out/stageproof /usr/local/bin/stageproof
USER stageproof
WORKDIR /work
ENTRYPOINT ["stageproof"]
