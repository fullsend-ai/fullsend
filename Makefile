.DEFAULT_GOAL := help
.PHONY: help bootstrap lint check fmt lint-adr-status lint-adr-numbers lint-adr-frontmatter
.PHONY: go-build go-test go-lint go-fmt go-vet go-tidy

help:
	@echo "Available targets:"
	@echo ""
	@echo "  Setup:"
	@echo "    help                 - Show this help message"
	@echo "    bootstrap            - Install all development tools"
	@echo ""
	@echo "  Go (CLI):"
	@echo "    go-build             - Build the fullsend CLI binary"
	@echo "    go-test              - Run Go unit tests with coverage"
	@echo "    go-lint              - Lint Go code with golangci-lint"
	@echo "    go-fmt               - Format Go code"
	@echo "    go-vet               - Run go vet"
	@echo "    go-tidy              - Tidy Go module dependencies"
	@echo ""
	@echo "  Python / Docs:"
	@echo "    check                - Run ruff and ty checks on Python"
	@echo "    fmt                  - Format Python code with ruff"
	@echo "    lint-adr-status      - Validate ADR statuses in all ADR files"
	@echo "    lint-adr-numbers     - Check for duplicate ADR numeric identifiers"
	@echo "    lint-adr-frontmatter - Validate ADR frontmatter and cross-references"
	@echo ""
	@echo "  Combined:"
	@echo "    lint                 - Run all linting (Go + Python + ADRs)"

# Install all development tools needed for linting, formatting, and pre-commit hooks.
# Prerequisites: uv (https://docs.astral.sh/uv/) and go (https://go.dev/)
#
# Installs tools to ~/.local/ so no root access is required.  Ensure
# ~/.local/bin is on your PATH (most distros include this by default).
BOOTSTRAP_TOOL_DIR := $(HOME)/.local/share/uv-tools
BOOTSTRAP_BIN_DIR  := $(HOME)/.local/bin

bootstrap:
	@mkdir -p "$(BOOTSTRAP_BIN_DIR)"
	@echo "==> Installing Python 3.12 (via uv)..."
	uv python install 3.12
	@echo "==> Installing ruff (linter/formatter)..."
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool install ruff || \
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool upgrade ruff
	@echo "==> Installing ty (type checker)..."
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool install ty || \
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool upgrade ty
	@echo "==> Installing pre-commit..."
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool install pre-commit || \
	UV_TOOL_DIR="$(BOOTSTRAP_TOOL_DIR)" UV_TOOL_BIN_DIR="$(BOOTSTRAP_BIN_DIR)" uv tool upgrade pre-commit
	@echo "==> Installing actionlint (GitHub Actions linter)..."
	GOBIN="$(BOOTSTRAP_BIN_DIR)" go install github.com/rhysd/actionlint/cmd/actionlint@latest
	@echo "==> Installing gitleaks (secret scanner)..."
	GOBIN="$(BOOTSTRAP_BIN_DIR)" go install github.com/zricethezav/gitleaks/v8@latest
	@echo "==> Installing golangci-lint..."
	GOBIN="$(BOOTSTRAP_BIN_DIR)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	@echo "==> Installing pre-commit hooks..."
	PATH="$(BOOTSTRAP_BIN_DIR):$(PATH)" pre-commit install
	@echo ""
	@echo "==> Bootstrap complete!"
	@echo "    Make sure $(BOOTSTRAP_BIN_DIR) is on your PATH."

## Combined lint target
lint: check go-lint go-vet lint-adr-status lint-adr-numbers lint-adr-frontmatter

## Go targets
GO_MODULE := github.com/fullsend-ai/fullsend
GO_BINARY := bin/fullsend
GO_LDFLAGS := -ldflags "-X $(GO_MODULE)/internal/cli.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)"

go-build:
	@echo "==> Building fullsend CLI..."
	go build $(GO_LDFLAGS) -o $(GO_BINARY) ./cmd/fullsend/
	@echo "    Binary: $(GO_BINARY)"

go-test:
	@echo "==> Running Go tests..."
	go test ./... -count=1 -cover -race

go-lint:
	@echo "==> Linting Go code..."
	golangci-lint run ./...

go-fmt:
	@echo "==> Formatting Go code..."
	gofmt -w -s cmd/ internal/

go-vet:
	@echo "==> Running go vet..."
	go vet ./...

go-tidy:
	@echo "==> Tidying Go modules..."
	go mod tidy

## Python / docs targets
check:
	uvx ruff check .
	uvx ty check hack/

fmt: go-fmt
	uvx ruff format .

lint-adr-status:
	@./hack/lint-adr-status

lint-adr-numbers:
	@./hack/lint-adr-numbers

lint-adr-frontmatter:
	@uv run --script ./hack/lint-adr-frontmatter
