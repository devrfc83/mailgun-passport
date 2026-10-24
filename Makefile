# Makefile for mailgun-passport
NAME := mailgun-passport
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +%Y%m%d%H%M%S)
GOOS ?= linux
GOARCH ?= amd64
LDFLAGS := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(BUILD_DATE)
PKG_DIR := build/pkg
RPM_DIR := $(PKG_DIR)/rpm
DEB_DIR := $(PKG_DIR)/deb
DIST_DIR := dist
IMAGE_NAME ?= mailgun-passport
REGISTRY ?= docker.io
DOCKERFILE ?= Dockerfile
CONTAINER_RUNTIME ?= auto

.PHONY: all build clean rpm deb rpm-all deb-all help
.PHONY: rpm-fedora43 rpm-fedora44 rpm-rocky8 rpm-rocky9 rpm-rocky10 rpm-alma8 rpm-alma9 rpm-alma10 rpm-rhel8 rpm-rhel9 rpm-rhel10
.PHONY: deb-ubuntu-24.04 deb-ubuntu-24.10 deb-ubuntu-25.04 deb-ubuntu-25.10 deb-ubuntu-26.04 deb-ubuntu-26.10 deb-debian13
.PHONY: docker-rpm podman-rpm docker-deb podman-deb docker-image podman-image _runtime

all: build

help:
	@echo "mailgun-passport build targets:"
	@echo "  make build       - Build binary locally"
	@echo "  make rpm         - Build RPMs for Fedora 43/44, Rocky/Alma/RHEL 8/9/10"
	@echo "  make deb         - Build DEBs for Ubuntu 24.04-26.10 and Debian 13"
	@echo "  make docker-rpm  - Build RPMs in Docker (no local rpm-build required)"
	@echo "  make podman-rpm  - Build RPMs in Podman (no local rpm-build required)"
	@echo "  make docker-deb  - Build DEBs in Docker (no local dpkg-buildpackage required)"
	@echo "  make podman-deb  - Build DEBs in Podman (no local dpkg-buildpackage required)"
	@echo "  make docker-image - Build Docker image"
	@echo "  make podman-image - Build Podman image"
	@echo "  make clean       - Remove build artifacts"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=$(VERSION) COMMIT=$(COMMIT) IMAGE_NAME=$(IMAGE_NAME) REGISTRY=$(REGISTRY)"

build:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST_DIR)/$(NAME) ./cmd/mailgun-passport
	@echo "Built $(DIST_DIR)/$(NAME)"

clean:
	rm -rf $(PKG_DIR) $(DIST_DIR) rpmbuild debian-build

$(PKG_DIR)/$(NAME)-$(VERSION).tar.gz: build
	@mkdir -p $(PKG_DIR)
	tar --transform "s,^,$(NAME)-$(VERSION)/," \
		--exclude='.git' \
		--exclude='build' \
		--exclude='dist' \
		--exclude='$(PKG_DIR)' \
		--exclude='rpmbuild' \
		--exclude='debian-build' \
		-czf $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz \
		cmd internal contrib README.md go.mod go.sum .env.example LICENSE 2>/dev/null || true

rpm: rpm-fedora43 rpm-fedora44 rpm-rocky8 rpm-rocky9 rpm-rocky10 rpm-alma8 rpm-alma9 rpm-alma10 rpm-rhel8 rpm-rhel9 rpm-rhel10
rpm-all: rpm

deb: deb-ubuntu-24.04 deb-ubuntu-24.10 deb-ubuntu-25.04 deb-ubuntu-25.10 deb-ubuntu-26.04 deb-ubuntu-26.10 deb-debian13
deb-all: deb

rpm-fedora43: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh fedora 43 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-fedora44: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh fedora 44 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rocky8: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rocky 8 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rocky9: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rocky 9 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rocky10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rocky 10 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-alma8: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh almalinux 8 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-alma9: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh almalinux 9 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-alma10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh almalinux 10 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rhel8: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rhel 8 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rhel9: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rhel 9 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

rpm-rhel10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-rpm.sh rhel 10 $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

deb-ubuntu-24.04: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 24.04 noble $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-ubuntu-24.10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 24.10 oracular $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-ubuntu-25.04: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 25.04 plucky $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-ubuntu-25.10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 25.10 questing $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-ubuntu-26.04: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 26.04 resolver $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-ubuntu-26.10: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh ubuntu 26.10 streaker $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

deb-debian13: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/build-deb.sh debian 13 trixie $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

_runtime:
	@if [ "$(CONTAINER_RUNTIME)" = "docker" ]; then \
		echo "docker"; \
	elif [ "$(CONTAINER_RUNTIME)" = "podman" ]; then \
		echo "podman"; \
	elif command -v podman >/dev/null 2>&1; then \
		echo "podman"; \
	elif command -v docker >/dev/null 2>&1; then \
		echo "docker"; \
	else \
		echo "none"; \
	fi

docker-rpm: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/docker-build-rpm.sh docker $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

podman-rpm: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/docker-build-rpm.sh podman $(VERSION) $(NAME) $(PKG_DIR) $(RPM_DIR)

docker-deb: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/docker-build-deb.sh docker $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

podman-deb: $(PKG_DIR)/$(NAME)-$(VERSION).tar.gz
	@bash scripts/docker-build-deb.sh podman $(VERSION) $(NAME) $(PKG_DIR) $(DEB_DIR)

docker-image:
	@echo "Building Docker image $(IMAGE_NAME):$(VERSION)..."
	@docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -f $(DOCKERFILE) -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .
	@echo "Built $(IMAGE_NAME):$(VERSION) and $(IMAGE_NAME):latest"
	@echo "Push with: docker tag $(IMAGE_NAME):latest $(REGISTRY)/$(IMAGE_NAME):latest && docker push $(REGISTRY)/$(IMAGE_NAME):latest"

podman-image:
	@echo "Building Podman image $(IMAGE_NAME):$(VERSION)..."
	@podman build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -f $(DOCKERFILE) -t $(IMAGE_NAME):$(VERSION) -t $(IMAGE_NAME):latest .
	@echo "Built $(IMAGE_NAME):$(VERSION) and $(IMAGE_NAME):latest"
	@echo "Push with: podman tag $(IMAGE_NAME):latest $(REGISTRY)/$(IMAGE_NAME):latest && podman push $(REGISTRY)/$(IMAGE_NAME):latest"
