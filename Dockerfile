# moedex default (zero-dependency) image.
#
# Builds the unified moedex shell as a fully static, CGO-free binary with NO build
# tags — i.e. no
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

# Build the single operational binary. No -tags => no ONNX, no cgo.
RUN go build -trimpath -ldflags="-s -w" -o /out/moedex ./cmd/moedex

# --- runtime ---------------------------------------------------------------
# alpine keeps a shell for HEALTHCHECK + `docker exec` debugging. The static
# CGO-free binary runs fine on musl. wget (busybox) drives the healthcheck.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && addgroup -S moedex && adduser -S -G moedex moedex
COPY --from=build /out/moedex /usr/local/bin/moedex

USER moedex

# Shard directory is mounted at runtime (read-only for -http; see deploy/README).
ENV MOEDEX_SHARD_DIR=/shards
EXPOSE 8080

# /healthz returns 200 "ok" and stays open even with auth on (auth excludes
# /healthz and /metrics). The daemon binds 0.0.0.0:8080 in-container (see CMD),
# which includes loopback, so this in-container probe reaches it.
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

# Bind 0.0.0.0 INSIDE the container: the container's network namespace is the
# isolation boundary, and a 127.0.0.1 bind here would be unreachable through a
# published port (-p). Publish only to the host loopback (-p 127.0.0.1:8080:8080)
# and front it with a trusted proxy. MOEDEX_AUTH_TOKEN is required for this
# non-loopback in-container bind; startup fails closed without it. For an
# isolated development container only, append --allow-insecure explicitly.
ENTRYPOINT ["moedex", "serve"]
CMD ["--shard-dir", "/shards", "--http", "0.0.0.0:8080"]
