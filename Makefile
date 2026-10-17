# Makefile for gswitch-auto (fork of gswitch; see docs/FORK.md).
#
#   make build       build builds/gswitch and builds/gswitch-tray (CGO)
#   make test        go test ./...
#   make vet         go vet ./...
#   make lint        golangci-lint run ./... (v2, config in .golangci.yml)
#   make rpm         build dist/gswitch-auto-<version>-<release>.x86_64.rpm
#                    and dist/checksums.txt (scripts/build-rpm.sh, runs tests)
#   make install     make rpm, then install it with sudo dnf
#   make ext-check   GNOME Shell extension consistency checks
#   make version     print the package version (scripts/version.sh)
#   make clean       remove builds/ and dist/
#
# Variables: GSWITCH_VERSION (default: scripts/version.sh), GSWITCH_RELEASE (1),
# NFPM (nfpm executable, default ~/go/bin/nfpm; nfpm on PATH or
# `go run ...nfpm@v2.48.0` is used when it does not exist), GO, BUILDDIR, DISTDIR.

GO ?= go
NFPM ?= $(HOME)/go/bin/nfpm
BUILDDIR ?= builds
DISTDIR ?= dist
GSWITCH_RELEASE ?= 1

ifeq ($(origin GSWITCH_VERSION),undefined)
GSWITCH_VERSION := $(shell scripts/version.sh 2>/dev/null)
endif

LDFLAGS := -s -w -X main.version=$(or $(GSWITCH_VERSION),dev)
NFPM_FLAG := $(if $(shell command -v '$(NFPM)' 2>/dev/null),--nfpm '$(NFPM)')
VERSION_FLAG := $(if $(GSWITCH_VERSION),--version '$(GSWITCH_VERSION)')
RPM_FILE := $(DISTDIR)/gswitch-auto-$(GSWITCH_VERSION)-$(GSWITCH_RELEASE).x86_64.rpm

.PHONY: all build test vet lint rpm install ext-check version clean

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

ext-check:
	scripts/check-extension.sh

version:
	@scripts/version.sh

clean:
	rm -rf $(BUILDDIR) $(DISTDIR)
