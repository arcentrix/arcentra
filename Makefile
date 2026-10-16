SHELL := /bin/bash
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
BUF_VERSION := v1.66.1
WIRE_VERSION := v0.7.0
PROTOC_GEN_GO_VERSION := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.5.1
BUF := $(LOCALBIN)/buf-$(BUF_VERSION)/buf
WIRE := $(LOCALBIN)/wire-$(WIRE_VERSION)/wire
PROTOC_GEN_GO := $(LOCALBIN)/protoc-gen-go-$(PROTOC_GEN_GO_VERSION)/protoc-gen-go
PROTOC_GEN_GO_GRPC := $(LOCALBIN)/protoc-gen-go-grpc-$(PROTOC_GEN_GO_GRPC_VERSION)/protoc-gen-go-grpc
PROTO_PLUGIN_PATH := $(dir $(PROTOC_GEN_GO)):$(dir $(PROTOC_GEN_GO_GRPC))

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

.PHONY: deps-check
deps-check: ## verify dependencies resolve from source without code generation
	go mod tidy -diff
	go mod verify

# -----------------------------------------------------------------------------
# Code generation
# -----------------------------------------------------------------------------
.PHONY: buf-install
buf-install: $(BUF) ## install the pinned Buf version

$(BUF):
	mkdir -p $(dir $(BUF))
	GOBIN=$(dir $(BUF)) go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)

.PHONY: proto-tools
proto-tools: $(BUF) $(PROTOC_GEN_GO) $(PROTOC_GEN_GO_GRPC) ## install pinned local protobuf generators

$(PROTOC_GEN_GO):
	mkdir -p $(dir $(PROTOC_GEN_GO))
	GOBIN=$(dir $(PROTOC_GEN_GO)) go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)

$(PROTOC_GEN_GO_GRPC):
	mkdir -p $(dir $(PROTOC_GEN_GO_GRPC))
	GOBIN=$(dir $(PROTOC_GEN_GO_GRPC)) go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)

.PHONY: buf
buf: proto-tools ## generate protobuf code locally with pinned plugins
	cd $(PROTO_DIR) && PATH="$(PROTO_PLUGIN_PATH):$$PATH" $(BUF) generate --template buf.gen.yaml

.PHONY: buf-check
buf-check: proto-tools ## check committed protobuf files against pinned generators
	PATH="$(PROTO_PLUGIN_PATH):$$PATH" bash scripts/check-protobuf.sh "$(BUF)" "$(PROTO_DIR)"

.PHONY: wire-install
wire-install: $(WIRE) ## install the pinned Wire version

$(WIRE):
	mkdir -p $(dir $(WIRE))
	GOBIN=$(dir $(WIRE)) go install github.com/google/wire/cmd/wire@$(WIRE_VERSION)

.PHONY: wire
wire: buf wire-install ## generate dependency injection code
	test -d $(CMD_PATH)
	if [ -f $(CMD_PATH)/wire.go ]; then cd $(CMD_PATH) && $(WIRE); fi

# -----------------------------------------------------------------------------
# Lint
# -----------------------------------------------------------------------------
.PHONY: golangci-lint-install
golangci-lint-install: $(LOCALBIN) ## install golangci-lint v2
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: lint
lint: golangci-lint-install ## run lint
	$(GOLANGCI_LINT) run

# -----------------------------------------------------------------------------
# Code generation (all-in-one)
# -----------------------------------------------------------------------------
.PHONY: codegen
codegen: buf wire-install ## run all code generators
	@for target in $(BINARIES); do \
		if [ -f cmd/$$target/wire.go ]; then (cd cmd/$$target && $(WIRE)); fi; \
	done

# -----------------------------------------------------------------------------
# Formatting / Testing
# -----------------------------------------------------------------------------
.PHONY: fmt-check
fmt-check: ## check Go code formatting
	@UNFMT=$$(gofmt -l $$(find . -name '*.go' ! -name '*.pb.go' ! -name 'wire_gen.go' ! -path '*/queries/*.go')); \
	if [ -n "$$UNFMT" ]; then echo "Unformatted files:"; echo "$$UNFMT"; exit 1; fi

.PHONY: test-git-env
test-git-env: ## verify test Git environment isolation
	bash scripts/with-clean-git-env.sh python3 scripts/test_clean_git_env.py

.PHONY: test
test: test-git-env ## run tests
	bash scripts/with-clean-git-env.sh go test -race -count=1 ./...

# -----------------------------------------------------------------------------
# Build
# -----------------------------------------------------------------------------
.PHONY: build
build: wire ## build binary (TARGET required)
	@echo "Building $(TARGET)..."
	go build -ldflags "$(LDFLAGS)" -o $(TARGET) $(CMD_PATH)

.PHONY: build-target
build-target: ## build release binary (no codegen, used by CI/Docker)
	go build -trimpath -ldflags "$(LDFLAGS) -s -w" -o $(TARGET) $(CMD_PATH)

.PHONY: run
run: wire ## run program
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
