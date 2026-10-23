# Build image for the gswitch-auto Debian 12 package (SPEC-X11 §3.8.3).
#
# Debian 12 ships Go 1.19 (backports: 1.23) while go.mod needs go 1.25.5, so
# the .deb is built with the official Go toolchain image on a bookworm
# userland: the CGO binaries then link against bookworm's glibc 2.36 and
# GTK 3.24, exactly what the target machine has.
#
#   make deb-image   podman build -t localhost/gswitch-auto-deb:bookworm \
#                      -f packaging/debian12.Containerfile packaging/
#   make deb         builds the image and runs scripts/build-deb.sh inside it
#
# Runtime conventions (scripts/build-deb.sh, docs/FORK.md): the checkout is
# mounted at /src, the container runs as the invoking user
# (--userns=keep-id, so HOME=/tmp and GOMODCACHE/GOCACHE point at writable
# named volumes), and nothing is copied into the image, so the build context
# (packaging/) is irrelevant. xvfb and x11-xkb-utils are for the gated X11
# tests (SPEC-X11 §6.2), not for the package build.
FROM docker.io/library/golang:1.26-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
      gcc libc6-dev pkg-config libgtk-3-dev libx11-dev libxkbcommon-dev \
      dpkg-dev fakeroot lintian python3 git ca-certificates xvfb x11-xkb-utils \
    && rm -rf /var/lib/apt/lists/*
RUN go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
ENV GOFLAGS=-buildvcs=false
WORKDIR /src
