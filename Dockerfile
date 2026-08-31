# syntax=docker/dockerfile:1

# Static, CGO-free binary — the same build the NAS release gets (see the
# Makefile's `nas` target), just cross-compiled for whatever platform
# `docker buildx` was asked for via the automatic TARGETOS/TARGETARCH args.
#
# --platform=$BUILDPLATFORM pins this stage to the runner's own architecture
# regardless of which target it's building for, so building linux/arm64 on
# an amd64 runner runs the Go toolchain natively and cross-compiles — which
# is what it's for — rather than running the whole compiler under QEMU. The
# difference is minutes, not seconds: emulating a compiler running is far
# slower than emulating the small binary it produces, which is the only
# part (the final COPY below) that actually needs to be arm64.
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/yamo ./cmd/yamo

# distroless static: no shell, no package manager, nothing to exploit if the
# API is ever tricked into running something — and it still carries CA
# certificates, which the Discogs cover lookup needs for outbound TLS, and a
# non-root user, which the -nonroot tag runs as by default.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/yamo /yamo

# /data and /music are conventions, not declarations — deliberately no
# VOLUME instruction, which would leave an anonymous volume behind at either
# path on any run that forgets to mount them, rather than failing loudly.
# Bind-mount both explicitly (see the compose file); /music is read-write,
# not read-only, because the API edits tags in the files it serves.
ENV YAMO_CATALOG=/data/catalog.db

EXPOSE 8467
ENTRYPOINT ["/yamo", "serve"]
CMD ["-listen", "0.0.0.0:8467"]
