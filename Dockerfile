# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /build

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o server ./cmd/server

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.19

# CA certs for HTTPS and timezone data.
RUN apk add --no-cache ca-certificates tzdata

# Copy the binary
COPY --from=builder /build/server /server

EXPOSE 8080 9090

ENTRYPOINT ["/server"]
