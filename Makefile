# Makefile for gswitch-auto (fork of gswitch; see docs/FORK.md).
#
#   make build       build builds/gswitch and builds/gswitch-tray (CGO)
#   make test        go test ./...
#   make vet         go vet ./...
#   make lint        golangci-lint run ./... (v2, config in .golangci.yml)
#   make rpm         build dist/gswitch-auto-<version>-<release>.x86_64.rpm
#                    and dist/checksums.txt (scripts/build-rpm.sh, runs tests)
#   make install     make rpm, then install it with sudo dnf
#   make deb         build dist/gswitch-auto_<version>-<revision>_amd64.deb and
#                    dist/checksums-deb.txt inside the Debian 12 podman image
#                    (scripts/build-deb.sh, runs tests and lintian)
#   make deb-image   build the Debian 12 image only (packaging/debian12.Containerfile)
#   make ext-check   GNOME Shell extension consistency checks
#   make version     print the package version (scripts/version.sh)
#   make clean       remove builds/ (including builds/deb) and dist/
#
# Variables: GSWITCH_VERSION (default: scripts/version.sh), GSWITCH_RELEASE (1),
# GSWITCH_DEB_REVISION (1), NFPM (nfpm executable, default ~/go/bin/nfpm; nfpm
# on PATH or `go run ...nfpm@v2.48.0` is used when it does not exist), PODMAN
# (podman), DEB_IMAGE (localhost/gswitch-auto-deb:bookworm), GO, BUILDDIR,
# DISTDIR.

GO ?= go
NFPM ?= $(HOME)/go/bin/nfpm
PODMAN ?= podman
DEB_IMAGE ?= localhost/gswitch-auto-deb:bookworm
BUILDDIR ?= builds
DISTDIR ?= dist
GSWITCH_RELEASE ?= 1
GSWITCH_DEB_REVISION ?= 1

ifeq ($(origin GSWITCH_VERSION),undefined)
GSWITCH_VERSION := $(shell scripts/version.sh 2>/dev/null)
endif

LDFLAGS := -s -w -X main.version=$(or $(GSWITCH_VERSION),dev)
NFPM_FLAG := $(if $(shell command -v '$(NFPM)' 2>/dev/null),--nfpm '$(NFPM)')
VERSION_FLAG := $(if $(GSWITCH_VERSION),--version '$(GSWITCH_VERSION)')
RPM_FILE := $(DISTDIR)/gswitch-auto-$(GSWITCH_VERSION)-$(GSWITCH_RELEASE).x86_64.rpm

.PHONY: all build test vet lint rpm install deb deb-image ext-check version clean

all: build

build:
	mkdir -p $(BUILDDIR)
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILDDIR)/gswitch ./cmd/gswitch
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BUILDDIR)/gswitch-tray ./cmd/gswitch-tray

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not found (CI uses v2.6.0, see .github/workflows/ci.yml)" >&2; exit 1; }
	golangci-lint run ./...

rpm:
	scripts/build-rpm.sh $(VERSION_FLAG) --release '$(GSWITCH_RELEASE)' --out '$(DISTDIR)' $(NFPM_FLAG)

install: rpm
	@test -n '$(GSWITCH_VERSION)' || { echo "cannot determine the version; set GSWITCH_VERSION" >&2; exit 1; }
	sudo dnf install '$(abspath $(RPM_FILE))'

# The .deb is built inside the Debian 12 image (Debian 12 has no Go 1.25+);
# scripts/build-deb.sh builds the image when needed and re-runs itself in it.
deb:
	PODMAN='$(PODMAN)' scripts/build-deb.sh $(VERSION_FLAG) --revision '$(GSWITCH_DEB_REVISION)' --out '$(DISTDIR)' --image '$(DEB_IMAGE)'

deb-image:
	$(PODMAN) build -t '$(DEB_IMAGE)' -f packaging/debian12.Containerfile packaging/

ext-check:
	scripts/check-extension.sh

version:
	@scripts/version.sh

clean:
	rm -rf $(BUILDDIR)/deb $(BUILDDIR) $(DISTDIR)
