# syntax=docker/dockerfile:1

# Base images are pinned to a patch version so a rebuild is reproducible and a
# base image change is a reviewable commit rather than a silent difference
# between two builds of the same source.
ARG GO_VERSION=1.26.8
ARG ALPINE_VERSION=3.22

# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:${GO_VERSION}-alpine AS builder

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /build

# Cache dependencies ahead of the source copy so a source-only change does not
# re-download the module graph.
COPY go.mod go.sum ./

# Corporate TLS interception breaks module download inside the container,
# because the proxy presents a certificate the image does not trust. The CA is
# passed as a BuildKit secret rather than copied into the context, so it never
# becomes an image layer and never reaches a registry:
#
#   docker build --secret id=corp_ca,src=$CACERTS .
#
# The secret is optional. Without it the mount is an empty file and the trust
# store is left alone, so the same Dockerfile builds on an unintercepted
# network unchanged.
RUN --mount=type=secret,id=corp_ca,target=/usr/local/share/ca-certificates/corp_ca.crt \
    if [ -s /usr/local/share/ca-certificates/corp_ca.crt ]; then update-ca-certificates; fi \
    && go mod download

COPY . .

# TARGETARCH is supplied by buildx. Leaving the architecture unpinned lets the
# same Dockerfile produce an amd64 or an arm64 image, which matters because
# Fargate on Graviton is materially cheaper for this workload.
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=unknown

# The development console is deliberately absent: it is behind the "devconsole"
# build tag and this build does not set it, so the unauthenticated /test page
# and the Entra device-code proxy are not compiled into the artifact.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o server ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:${ALPINE_VERSION}

# ca-certificates is required: every outbound call, to login.microsoftonline.com
# for token exchange and to graph.microsoft.com for data, is TLS.
RUN apk add --no-cache ca-certificates tzdata

# Trust the same corporate CA at run time when one is supplied. An environment
# that intercepts egress TLS would otherwise fail every token exchange and
# every Graph call with an unknown-authority error. update-ca-certificates runs
# during the build so the result is a layer; the secret itself is not.
RUN --mount=type=secret,id=corp_ca,target=/usr/local/share/ca-certificates/corp_ca.crt \
    if [ -s /usr/local/share/ca-certificates/corp_ca.crt ]; then update-ca-certificates; fi

# Run as an unprivileged account. The process needs no privileged port, no
# package manager and no write access to the image, so root buys nothing and
# turns any remote code execution into immediate container-level control.
RUN addgroup -g 10001 -S msgraph \
    && adduser -u 10001 -S -G msgraph -h /nonexistent -s /sbin/nologin msgraph

COPY --from=builder /build/server /server

# The tool exposure policy is baked into the image so that the surface a
# deployment exposes is version controlled alongside it rather than mounted at
# run time. Override the path with TOOL_POLICY_FILE if you prefer to supply it
# from a volume or a config store.
COPY --chown=10001:10001 tools.example.yaml /etc/msgraph-mcp/tools.example.yaml

USER 10001:10001

EXPOSE 8080 9090

# The container owns no state, so it can run with a read-only root filesystem.
# Set readonlyRootFilesystem in the ECS task definition.
ENTRYPOINT ["/server"]
