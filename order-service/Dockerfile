# syntax=docker/dockerfile:1

# --- build stage ---------------------------------------------------------
FROM golang:1.27-alpine AS build

WORKDIR /app

# Dependencies are copied and downloaded first so this layer is cached and only
# invalidated when go.mod or go.sum actually change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on a distroless/scratch base.
# -trimpath keeps build-machine paths out of the binary; -s -w drops the symbol
# table and DWARF data, which meaningfully shrinks the image.
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w -X main.version=${VERSION}" \
        -o /out/api ./cmd/api

# --- runtime stage -------------------------------------------------------
FROM alpine:3.21 AS prod

# ca-certificates is required for outbound TLS (OTLP collectors, upstream APIs).
# tzdata keeps time.LoadLocation working.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 -H app

WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /app/migrations /app/migrations

# Running as a non-root user is the single cheapest container hardening step.
USER 10001

ENV PORT=8080
EXPOSE 8080

# Exec form: the binary becomes PID 1 and receives SIGTERM directly, which is
# what the graceful shutdown in cmd/api/main.go waits for.
ENTRYPOINT ["/app/api"]
