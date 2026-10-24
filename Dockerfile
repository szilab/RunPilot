# syntax=docker/dockerfile:1
#
# RunPilot container image.
#
#   docker build -t runpilot .
#   docker run -d -p 9070:9070 \
#     -v /var/lib/runpilot:/var/lib/runpilot \
#     -v /var/run/docker.sock:/var/run/docker.sock \
#     runpilot
#
# See packaging/docker/compose.yaml and "Running in Docker" in README.md.

FROM --platform=$BUILDPLATFORM golang:1.24-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS=linux
ARG TARGETARCH
# VERSION overrides the built-in development version for release images.
ARG VERSION=""
RUN set -eu; \
    ldflags="-s -w -X github.com/szilab/RunPilot/internal/version.Deployment=docker"; \
    if [ -n "$VERSION" ]; then ldflags="$ldflags -X github.com/szilab/RunPilot/internal/version.Version=$VERSION"; fi; \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -ldflags="$ldflags" -o /out/runpilot ./cmd/runpilot

FROM alpine:3.23
# Runtime tools used by first-party plugins and common tasks:
#   docker-cli, docker-cli-compose  Docker plugin (Compose v2)
#   bash                            Terminal default shell, Tasks "bash" interpreter
#   python3                         Tasks "python" interpreter
#   openssh-client                  ssh/scp from Tasks and Terminal
#   tini, su-exec, shadow           init process and privilege drop in the entrypoint
#   ca-certificates, tzdata         plugin catalog over HTTPS, scheduler time zones
RUN apk add --no-cache \
        bash ca-certificates docker-cli docker-cli-compose openssh-client \
        python3 shadow su-exec tini tzdata \
    && addgroup -g 1000 runpilot \
    && adduser -D -u 1000 -G runpilot -h /home/runpilot -s /bin/bash runpilot \
    && install -d -o runpilot -g runpilot /var/lib/runpilot
COPY --from=build /out/runpilot /usr/local/bin/runpilot
COPY --chmod=0755 packaging/docker/entrypoint.sh /usr/local/bin/runpilot-entrypoint

# Mount the data directory at the same path on the host so Docker Compose
# projects with relative bind mounts resolve to real host paths.
ENV RUNPILOT_DATA_DIR=/var/lib/runpilot \
    RUNPILOT_CONTAINER=1 \
    SHELL=/bin/bash
WORKDIR /home/runpilot
EXPOSE 9070

LABEL org.opencontainers.image.title="RunPilot" \
      org.opencontainers.image.description="Plugin-based host management web application" \
      org.opencontainers.image.source="https://github.com/szilab/RunPilot"

# tini reaps orphaned processes started by tasks and terminals, so no
# "docker run --init" is required.
ENTRYPOINT ["/sbin/tini", "--", "/usr/local/bin/runpilot-entrypoint"]
CMD ["run", "--bind-address", "0.0.0.0", "--port", "9070"]
