SHELL := /bin/sh
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# -----------------------------------------------------------------------------
# Version / build metadata
# -----------------------------------------------------------------------------
CURRENT_YEAR := $(shell date +%y)
VERSION_FILE := $(shell if [ -f VERSION ]; then cat VERSION | tr -d '[:space:]\n\r'; fi)
GIT_TAG := $(shell git describe --tags --exact-match 2>/dev/null || echo "")

ifeq ($(VERSION),)
  ifneq ($(VERSION_FILE),)
    VERSION := $(VERSION_FILE)
  else ifneq ($(GIT_TAG),)
    VERSION := $(shell echo $(GIT_TAG) | sed 's/^v//')
  else
    VERSION := $(CURRENT_YEAR).0.0.0
  endif
endif

GIT_BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
GIT_COMMIT := $(shell git rev-parse HEAD)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := \
 -X 'github.com/arcentrix/arcentra/pkg/version.Version=$(VERSION)' \
 -X 'github.com/arcentrix/arcentra/pkg/version.GitBranch=$(GIT_BRANCH)' \
 -X 'github.com/arcentrix/arcentra/pkg/version.GitCommit=$(GIT_COMMIT)' \
 -X 'github.com/arcentrix/arcentra/pkg/version.BuildTime=$(BUILD_TIME)'

ifneq ($(RELEASE),1)
  LDFLAGS_STRIP :=
else
  LDFLAGS_STRIP := -s -w
endif

LDFLAGS += $(LDFLAGS_STRIP)

# -----------------------------------------------------------------------------
# Common variables
# -----------------------------------------------------------------------------
TARGET ?= arcentra
CMD_PATH := ./cmd/$(TARGET)

PROTO_DIR ?= api
LOCALBIN ?= $(shell pwd)/bin

IMG ?= ghcr.io/arcentrix/arcentra:latest
CONTAINER_TOOL ?= docker
PLATFORMS ?= linux/arm64,linux/amd64

JOBS ?= $(shell getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)

BINARIES := $(notdir $(wildcard cmd/*))

# -----------------------------------------------------------------------------
# Tools
# -----------------------------------------------------------------------------
GOLANGCI_LINT := $(LOCALBIN)/golangci-lint

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# -----------------------------------------------------------------------------
# Help
# -----------------------------------------------------------------------------
.PHONY: help
help: ## show this help
	@echo "Arcentra Build System"
	@echo ""
	@echo "Usage:"
	@echo "  make <command> [TARGET=name]"
	@echo ""
	@echo "Targets:"
	@for b in $(BINARIES); do echo "  $$b"; done
	@echo ""
	@echo "Commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
	awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

# -----------------------------------------------------------------------------
# Dependency management
# -----------------------------------------------------------------------------
.PHONY: deps
deps: ## sync go dependencies
	go mod tidy
	go mod verify

# -----------------------------------------------------------------------------
# Code generation
# -----------------------------------------------------------------------------
.PHONY: buf-install
buf-install: ## install buf if missing
	@command -v buf >/dev/null 2>&1 || go install github.com/bufbuild/buf/cmd/buf@latest

.PHONY: buf
buf: buf-install ## generate protobuf code
	cd $(PROTO_DIR) && buf generate --template buf.gen.yaml

.PHONY: wire-install
wire-install: ## install wire
	@command -v wire >/dev/null 2>&1 || go install github.com/google/wire/cmd/wire@latest

.PHONY: wire
wire: wire-install ## generate dependency injection code
	test -d $(CMD_PATH)
	cd $(CMD_PATH) && wire

.PHONY: sqlc-install
sqlc-install: ## install sqlc
	@command -v sqlc >/dev/null 2>&1 || go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest

.PHONY: sqlc
sqlc: sqlc-install ## generate sql code
	sqlc generate

# -----------------------------------------------------------------------------
# Lint
# -----------------------------------------------------------------------------
.PHONY: golangci-lint-install
golangci-lint-install: $(LOCALBIN) ## install golangci-lint
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

.PHONY: lint
lint: golangci-lint-install ## run lint
	$(GOLANGCI_LINT) run

# -----------------------------------------------------------------------------
# Build
# -----------------------------------------------------------------------------
.PHONY: build
build: wire buf sqlc ## build binary (TARGET required)
	@echo "Building $(TARGET)..."
	go build -ldflags "$(LDFLAGS)" -o $(TARGET) $(CMD_PATH)

.PHONY: run
run: wire buf sqlc ## run program
	go run -ldflags "$(LDFLAGS)" $(CMD_PATH)

# -----------------------------------------------------------------------------
# Container
# -----------------------------------------------------------------------------
.PHONY: docker-build
docker-build: ## build container image
	$(CONTAINER_TOOL) build \
		--target $(TARGET) \
		--build-arg TARGET=$(TARGET) \
		-t $(IMG) .

.PHONY: docker-push
docker-push: ## push image
	$(CONTAINER_TOOL) push $(IMG)

.PHONY: docker-buildx
docker-buildx: ## multi arch build
	$(CONTAINER_TOOL) run --rm --privileged tonistiigi/binfmt --install all
	$(CONTAINER_TOOL) buildx create --use --name arcentra-builder || true
	$(CONTAINER_TOOL) buildx build \
		--platform=$(PLATFORMS) \
		--target $(TARGET) \
		--build-arg TARGET=$(TARGET) \
		--tag $(IMG) \
		--push \
		.

# -----------------------------------------------------------------------------
# Static analysis
# -----------------------------------------------------------------------------
.PHONY: staticcheck-install
staticcheck-install: ## install staticcheck
	@command -v staticcheck >/dev/null 2>&1 || go install honnef.co/go/tools/cmd/staticcheck@latest

.PHONY: staticcheck
staticcheck: staticcheck-install ## run staticcheck
	staticcheck ./...

# -----------------------------------------------------------------------------
# Version
# -----------------------------------------------------------------------------
.PHONY: version
version: ## show version
	@echo "Version: $(VERSION)"
	@echo "Branch: $(GIT_BRANCH)"
	@echo "Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

.PHONY: version-tag
version-tag: ## create git tag
	git tag -a v$(VERSION) -m "Release $(VERSION)"
	echo "tag created: v$(VERSION)"

# -----------------------------------------------------------------------------
# license
# -----------------------------------------------------------------------------
.PHONY: addlicense-install
addlicense-install: ## install addlicense tool
	@command -v addlicense >/dev/null 2>&1 || { \
		echo ">> addlicense not found, installing..."; \
		go install github.com/onexstack/addlicense@latest; \
	}
	@echo ">> addlicense installed: $$(which addlicense)"

.PHONY: addlicense
addlicense: addlicense-install ## run addlicense (add headers to .go files, skip generated)
	@echo ">> running addlicense..."
	@addlicense -v -l apache -c "Arcentra Authors." \
		--skip-files "wire_gen\.go" \
		--skip-files "\.pb\.go$$" \
		--skip-files "_grpc\.pb\.go$$" \
		--skip-dirs "./idea" \
		--skip-dirs "./.vscode" \
		--skip-dirs "./.cursor" \
		.
	@echo ">> addlicense done."