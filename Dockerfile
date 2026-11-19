# Stageproof, Helm, and Git. Enough for a file check.
# --live uses this image too. Docker, kind, and kubectl stay on the host
# and are mounted in, with the Docker socket and /tmp. See the README.
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
 && git config --system --add safe.directory '*' \
 && useradd --create-home --uid 65532 stageproof
COPY --from=build /out/stageproof /usr/local/bin/stageproof
USER stageproof
WORKDIR /work
ENTRYPOINT ["stageproof"]
