# syntax=docker/dockerfile:1

FROM oven/bun:1-alpine AS ui-builder
WORKDIR /src/web
COPY web/package.json web/bun.lock* ./
RUN bun install --frozen-lockfile || bun install
COPY web/ ./
RUN bun run build

FROM golang:alpine AS go-builder
ENV GOTOOLCHAIN=auto CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=ui-builder /src/internal/server/ui/dist ./internal/server/ui/dist
RUN go build -ldflags="-s -w" -o /out/mw-echo ./cmd/mw-echo

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=go-builder /out/mw-echo /usr/local/bin/mw-echo

ENV MW_ECHO_ADDR="0.0.0.0:4310" \
    MW_ECHO_DATA_DIR="/data"

EXPOSE 4310
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:4310/health" || exit 1

ENTRYPOINT ["/usr/local/bin/mw-echo"]
