# moedex default (zero-dependency) image.
#
# Builds the warm retrieval daemon (moedex-serve) and the offline shard tool
# (moedex-index) as fully static, CGO-free binaries with NO build tags — i.e. no
# ONNX, no dense arm. This is the identity image: pure-Go lexical + symbol +
# path retrieval with zero runtime dependencies. For the optional in-process
# dense (ONNX) embedder, use Dockerfile.dense instead.
#
# Build:
#   docker build -t moedex-serve .
# Run (loopback-only publish, shards mounted read-only for the -http daemon):
#   docker run --rm -p 127.0.0.1:8080:8080 \
#     -v /srv/moedex/shards:/shards:ro \
#     -e MOEDEX_AUTH_TOKEN=... \
#     moedex-serve
#
# NOTE: the plain daemon has no `make` build target that emits a single binary
# (`make build` is `go build ./...` with no -o), so we invoke `go build -o`
# directly here. This is a known gap, reported by Lane C — not added to the
# Makefile.

# --- builder ---------------------------------------------------------------
# golang:1.26-bookworm matches go.mod (`go 1.26`); bookworm = Debian 12.
FROM golang:1.26-bookworm AS build
WORKDIR /src

# Avoid surprise toolchain downloads: pin to the toolchain in the base image.
ENV GOTOOLCHAIN=local
ENV CGO_ENABLED=0

# Warm the module cache first for better layer reuse. The go.mod `replace`
# points github.com/sugarme/tokenizer at a public github.com fork, so module
# download needs network access at build time.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build both operationally relevant binaries. No -tags => no ONNX, no cgo.
RUN go build -trimpath -ldflags="-s -w" -o /out/moedex-serve ./cmd/moedex-serve \
 && go build -trimpath -ldflags="-s -w" -o /out/moedex-index ./cmd/moedex-index

# --- runtime ---------------------------------------------------------------
# alpine keeps a shell for HEALTHCHECK + `docker exec` debugging. The static
# CGO-free binary runs fine on musl. wget (busybox) drives the healthcheck.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && addgroup -S moedex && adduser -S -G moedex moedex
COPY --from=build /out/moedex-serve /usr/local/bin/moedex-serve
COPY --from=build /out/moedex-index /usr/local/bin/moedex-index

USER moedex

# Shard directory is mounted at runtime (read-only for -http; see deploy/README).
ENV MOEDEX_SHARD_DIR=/shards
EXPOSE 8080

# /healthz returns 200 "ok" and stays open even with auth on (auth excludes
# /healthz and /metrics). The daemon binds 127.0.0.1 by default (resolveAddr),
# so probe the in-container loopback explicitly.
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

# Bind 127.0.0.1 explicitly (the daemon would default to loopback anyway) and
# publish only to the host loopback (-p 127.0.0.1:8080:8080) behind a trusted
# proxy. Pass MOEDEX_AUTH_TOKEN to require Bearer auth on /search and /stats.
ENTRYPOINT ["moedex-serve"]
CMD ["-shard-dir", "/shards", "-http", "127.0.0.1:8080"]
