SHELL := /bin/sh
.ONESHELL:
.SHELLFLAGS := -eu -o pipefail -c

.DEFAULT_GOAL := help

# -----------------------------------------------------------------------------
# Version / build metadata
# -----------------------------------------------------------------------------
# Version format: YY.Major.Minor.Patch (e.g., 25.1.2.3, where 25 represents 2025)
# Priority: 1) VERSION env var, 2) VERSION file, 3) git tag, 4) default (YY.0.0.0)
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

# -----------------------------------------------------------------------------
# Common vars
# -----------------------------------------------------------------------------
JOBS ?= $(shell getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)

PROTO_DIR ?= api
## Target name (also Dockerfile stage and binary name): arcentra / arcentra-agent
TARGET ?= arcentra 

## Container image name (override with IMG=repo/name:tag)
IMG ?= arcentra:latest

## Container tool (docker/podman)
CONTAINER_TOOL ?= docker

## Multi-arch platforms for docker-buildx
PLATFORMS ?= linux/arm64,linux/amd64

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
GOLANGCI_LINT := $(LOCALBIN)/golangci-lint

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# -----------------------------------------------------------------------------
# Help
# -----------------------------------------------------------------------------
.PHONY: help ## show help information
help:
	@echo "arcentra CI/CD platform Makefile commands"
	@echo ""
	@echo "Usage: make [command]"
	@echo ""
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "Examples:"
	@echo "  make buf-install    # install buf tool if missing"
	@echo "  make buf            # generate proto code by buf"
	@echo "  make all            # full build"

# -----------------------------------------------------------------------------
# Dependencies / lint
# -----------------------------------------------------------------------------
.PHONY: deps-sync ## sync dependencies
deps-sync:
	go mod tidy
	go mod verify

.PHONY: golangci-lint ## ensure golangci-lint exists in LOCALBIN
golangci-lint: $(GOLANGCI_LINT)

$(GOLANGCI_LINT): | $(LOCALBIN)
	@echo ">> installing golangci-lint to $(LOCALBIN)..."
	@GOBIN="$(LOCALBIN)" go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@echo ">> golangci-lint installed: $(GOLANGCI_LINT)"

.PHONY: lint ## Run golangci-lint linter
lint: golangci-lint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix ## Run golangci-lint linter and perform fixes
lint-fix: golangci-lint
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config ## Verify golangci-lint linter configuration
lint-config: golangci-lint
	$(GOLANGCI_LINT) config verify

# -----------------------------------------------------------------------------
# Build / run
# -----------------------------------------------------------------------------
.PHONY: all ## full build (prebuild+main program)
all: deps-sync prebuild build

.PHONY: prebuild ## prepare assets before build (optional)
prebuild:
	@if [ -x ./dl.sh ]; then \
		echo ">> running ./dl.sh ..."; \
		./dl.sh; \
	elif [ -x ./scripts/prebuild.sh ]; then \
		echo ">> running ./scripts/prebuild.sh ..."; \
		./scripts/prebuild.sh; \
	else \
		echo ">> prebuild: no prebuild script found, skip."; \
	fi

.PHONY: build ## build main program
build: TARGET=arcentra
build: wire buf
	go build -ldflags "${LDFLAGS}" -o arcentra ./cmd/arcentra/

.PHONY: build-agent ## build agent program
build-agent: TARGET=arcentra-agent
build-agent: wire buf
	go build -ldflags "${LDFLAGS}" -o arcentra-agent ./cmd/arcentra-agent/

.PHONY: build-target ## build selected target binary (TARGET=arcentra|arcentra-agent)
build-target: wire buf
	go build -ldflags "${LDFLAGS}" -o "$(TARGET)" ./cmd/"$(TARGET)"/

.PHONY: build-cli ## build CLI tool
build-cli:
	go build -ldflags "${LDFLAGS}" -o arcentra-cli ./cmd/cli/

.PHONY: run ## run main program
run: TARGET=arcentra
run: deps-sync wire buf
	go run -ldflags "${LDFLAGS}" ./cmd/arcentra/

.PHONY: run-agent ## run agent program
run-agent: TARGET=arcentra-agent
run-agent: deps-sync wire buf
	go run ./cmd/arcentra-agent/

.PHONY: run-cli ## run CLI tool
run-cli: deps-sync wire buf
	go run ./cmd/cli/

.PHONY: release ## create release version
release:
	goreleaser --skip-validate --skip-publish --snapshot

# -----------------------------------------------------------------------------
# Container images
# -----------------------------------------------------------------------------
.PHONY: docker-build ## Build container image (uses Dockerfile)
docker-build:
	$(CONTAINER_TOOL) build --target $(TARGET) --build-arg TARGET=$(TARGET) -t $(IMG) .

.PHONY: docker-push ## Push container image to registry
docker-push:
	$(CONTAINER_TOOL) push $(IMG)

.PHONY: docker-buildx ## Build and push multi-arch image (requires buildx)
docker-buildx:
	$(CONTAINER_TOOL) run --rm --privileged tonistiigi/binfmt --install all
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name arcentra-builder
	$(CONTAINER_TOOL) buildx use arcentra-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --provenance=false --target $(TARGET) --build-arg TARGET=$(TARGET) --tag $(IMG) -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm arcentra-builder
	rm -f Dockerfile.cross

# -----------------------------------------------------------------------------
# Proto / code generation
# -----------------------------------------------------------------------------
.PHONY: buf-install ## ensure buf is installed (install if missing)
buf-install:
	@command -v buf >/dev/null 2>&1 || { \
		echo ">> buf not found, installing..."; \
		go install github.com/bufbuild/buf/cmd/buf@latest; \
	}
	@echo ">> buf installed: $$(which buf)"

.PHONY: buf ## generate buf code
buf: buf-install
	@echo ">> generating buf code from $(PROTO_DIR)"
	@cd $(PROTO_DIR) && buf generate --template buf.gen.yaml
	@echo ">> buf code generation done."

.PHONY: buf-lint ## check buf code style
buf-lint: buf-install
	@echo ">> linting buf code..."
	@cd $(PROTO_DIR) && buf lint
	@echo ">> buf code linting done."

.PHONY: buf-breaking ## check buf code breaking changes
buf-breaking: buf-install
	@echo ">> checking buf code breaking changes..."
	@cd $(PROTO_DIR) && buf breaking
	@echo ">> buf code breaking changes checking done."

.PHONY: buf-push ## push buf code
buf-push: buf-install
	@echo ">> pushing buf code..."
	@cd $(PROTO_DIR) && buf push
	@echo ">> buf code pushing done."

.PHONY: buf-clean ## clean generated buf code
buf-clean:
	@echo ">> cleaning generated protobuf files..."
	@find $(PROTO_DIR) -type f \( -name "*.pb.go" -o -name "*_grpc.pb.go" \) -delete 2>/dev/null || true
	@echo ">> protobuf files cleaned."

.PHONY: wire-install ## install wire tool
wire-install:
	@command -v wire >/dev/null 2>&1 || { \
		echo ">> wire not found, installing..."; \
		go install github.com/google/wire/cmd/wire@latest; \
	}
	@echo ">> wire installed: $$(which wire)"

.PHONY: wire ## generate wire dependency injection code
wire: wire-install
	@if [ -z "$(TARGET)" ]; then \
		echo "error: TARGET is empty (expected arcentra or arcentra-agent)"; \
		exit 1; \
	fi
	@echo ">> generating wire code for $(TARGET)..."
	@cd cmd/$(TARGET) && wire
	@echo ">> wire code generation done."

.PHONY: wire-clean ## clean wire generated code
wire-clean:
	@echo ">> cleaning wire generated files..."
	@find . -name "wire_gen.go" -type f -delete
	@echo ">> wire files cleaned."

# -----------------------------------------------------------------------------
# Static analysis / code quality
# -----------------------------------------------------------------------------
.PHONY: staticcheck-install ## ensure staticcheck is installed (install if missing)
staticcheck-install:
	@command -v staticcheck >/dev/null 2>&1 || { \
		echo ">> staticcheck not found, installing..."; \
		go install honnef.co/go/tools/cmd/staticcheck@latest; \
	}
	@echo ">> staticcheck installed: $$(which staticcheck)"

.PHONY: staticcheck ## run staticcheck code analysis
staticcheck: staticcheck-install
	@echo ">> running staticcheck..."
	@staticcheck ./...
	@echo ">> staticcheck analysis done."

.PHONY: addlicense-install ## install addlicense tool
addlicense-install:
	@command -v addlicense >/dev/null 2>&1 || { \
		echo ">> addlicense not found, installing..."; \
		go install github.com/onexstack/addlicense@latest; \
	}
	@echo ">> addlicense installed: $$(which addlicense)"

.PHONY: addlicense ## run addlicense code analysis
addlicense: addlicense-install
	@echo ">> running addlicense..."
	@addlicense -v -l apache -c "Arcentra Authors." $(find . -name "*.go" -not -name "wire_gen.go" -not -name "*.pb.go" -not -name "*_grpc.pb.go")
	@echo ">> addlicense analysis done."

# -----------------------------------------------------------------------------
# Version management
# -----------------------------------------------------------------------------
.PHONY: version ## show current version information
version:
	@echo "Current Version: $(VERSION)"
	@if [ -f VERSION ]; then \
		echo "VERSION file: $$(cat VERSION | tr -d '[:space:]')"; \
	else \
		echo "VERSION file: not found"; \
	fi
	@echo "Git Tag: $(GIT_TAG)"
	@echo "Git Branch: $(GIT_BRANCH)"
	@echo "Git Commit: $(GIT_COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"

.PHONY: version-check ## validate version format (YY.Major.Minor.Patch)
version-check:
	@echo ">> validating version format: $(VERSION)"
	@if ! echo "$(VERSION)" | grep -qE '^[0-9]{2}\.[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "error: invalid version format: $(VERSION)"; \
		echo "expected format: YY.Major.Minor.Patch (e.g., 25.1.2.3, where 25 represents 2025)"; \
		exit 1; \
	fi
	@echo ">> version format is valid: $(VERSION)"

.PHONY: version-tag ## create git tag with current version
version-tag: version-check
	@echo ">> creating git tag: v$(VERSION)"
	@git tag -a "v$(VERSION)" -m "Release version $(VERSION)"
	@echo ">> git tag created: v$(VERSION)"
	@echo ">> to push tag, run: git push origin v$(VERSION)"