# Makefile for mailgun-passport
NAME       := mailgun-passport
VERSION    ?= $(shell scripts/version.sh)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell git log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
ARCHES     ?= amd64 arm64
DIST       := dist
LDFLAGS    := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)
GOBUILD    := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

NFPM       ?= go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0
GO_IMAGE   ?= docker.io/library/golang:1.27
IMAGE      ?= $(NAME)
# Container image tags cannot contain "+".
IMAGE_TAG  ?= $(subst +,-,$(VERSION))

# Podman is preferred; set CONTAINER=docker to override.
CONTAINER  ?= $(notdir $(shell command -v podman 2>/dev/null || command -v docker 2>/dev/null))
# Rootful Docker would leave root-owned files in dist/; rootless Podman maps
# container root to the invoking user already.
ifeq ($(CONTAINER),docker)
CONTAINER_USER := --user $(shell id -u):$(shell id -g)
endif
# Podman's default OCI image format drops the Dockerfile's HEALTHCHECK.
ifeq ($(CONTAINER),podman)
IMAGE_FORMAT := --format docker
endif

export VERSION

.PHONY: all help build test binaries packages rpm deb container-packages test-packages image clean _need-container FORCE

all: build

help:
	@echo "mailgun-passport build targets:"
	@echo "  make build               Build dist/$(NAME) for this machine"
	@echo "  make test                Run go vet and the test suite"
	@echo "  make packages            Build .rpm and .deb for: $(ARCHES)"
	@echo "  make rpm | make deb      Build only one package format"
	@echo "  make container-packages  Build packages inside $(GO_IMAGE) (needs only podman/docker)"
	@echo "  make test-packages       Install the packages in Fedora, EL, Debian and Ubuntu containers"
	@echo "  make image               Build the container image $(IMAGE):$(IMAGE_TAG)"
	@echo "  make clean               Remove $(DIST)/"
	@echo ""
	@echo "VERSION=$(VERSION) COMMIT=$(COMMIT) CONTAINER=$(CONTAINER)"

build:
	$(GOBUILD) -o $(DIST)/$(NAME) ./cmd/$(NAME)

test:
	go vet ./...
	go test -race -count=1 ./...

binaries: $(foreach arch,$(ARCHES),$(DIST)/linux_$(arch)/$(NAME))

# FORCE: go build decides what is up to date.
$(DIST)/linux_%/$(NAME): FORCE
	GOOS=linux GOARCH=$* $(GOBUILD) -o $@ ./cmd/$(NAME)

packages: rpm deb

rpm deb: binaries
	@for arch in $(ARCHES); do \
		echo "nfpm: $@ $$arch $(VERSION)"; \
		ARCH=$$arch $(NFPM) package --config packaging/nfpm.yaml --packager $@ --target $(DIST)/ || exit 1; \
	done

_need-container:
	@test -n "$(CONTAINER)" || { echo "podman or docker is required" >&2; exit 1; }

# :Z relabels the bind mount so SELinux lets the container read it.
container-packages: _need-container
	$(CONTAINER) run --rm $(CONTAINER_USER) \
		-v "$(CURDIR):/src:Z" -w /src \
		-e HOME=/tmp -e GOCACHE=/tmp/go-cache -e GOPATH=/tmp/go -e GOFLAGS=-buildvcs=false \
		$(GO_IMAGE) \
		make packages VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" ARCHES="$(ARCHES)"

test-packages: _need-container
	scripts/test-packages.sh "$(CONTAINER)" "$(DIST)"

image: _need-container
	$(CONTAINER) build $(IMAGE_FORMAT) \
		--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_DATE="$(BUILD_DATE)" \
		-t $(IMAGE):$(IMAGE_TAG) -t $(IMAGE):latest .

clean:
	rm -rf $(DIST)
