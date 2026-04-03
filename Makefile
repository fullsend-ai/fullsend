.DEFAULT_GOAL := help
.PHONY: help bootstrap lint check fmt lint-adr-status lint-adr-numbers lint-adr-frontmatter mindmap \
       go-build go-test go-lint go-fmt go-vet go-tidy e2e-test e2e-playwright

help:
	@echo "Available targets:"
	@echo "  help                 - Show this help message"
	@echo "  bootstrap            - Install all development tools"
	@echo "  lint                 - Run all linting and validation"
	@echo "  check                - Run ruff and ty checks on Python"
	@echo "  fmt                  - Format Python code with ruff"
	@echo "  lint-adr-status      - Validate ADR statuses in all ADR files"
	@echo "  lint-adr-numbers     - Check for duplicate ADR numeric identifiers"
	@echo "  lint-adr-frontmatter - Validate ADR frontmatter and cross-references"
	@echo "  mindmap              - Open the interactive document graph in a browser"
	@echo "  go-build             - Build the fullsend binary"
	@echo "  go-test              - Run Go tests with race detection and coverage"
	@echo "  go-lint              - Run golangci-lint"
	@echo "  go-fmt               - Format Go code"
	@echo "  go-vet               - Run go vet"
	@echo "  go-tidy              - Run go mod tidy"
	@echo "  e2e-test             - Run admin e2e tests (requires E2E_GITHUB_USERNAME and E2E_GITHUB_PASSWORD or E2E_GITHUB_PASSWORD_FILE)"

# Install all development tools needed for linting, formatting, and pre-commit hooks.
# Prerequisites: uv (https://docs.astral.sh/uv/) and go (https://go.dev/)
#
# Installs tools to ~/.local/ so no root access is required.  Ensure
# ~/.local/bin is on your PATH (most distros include this by default).
BOOTSTRAP_TOOL_DIR := $(HOME)/.local/share/uv-tools
BOOTSTRAP_BIN_DIR  := $(HOME)/.local/bin

bootstrap: e2e-playwright
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
	@echo "==> Installing pre-commit hooks..."
	PATH="$(BOOTSTRAP_BIN_DIR):$(PATH)" pre-commit install
	@echo ""
	@echo "==> Bootstrap complete!"
	@echo "    Make sure $(BOOTSTRAP_BIN_DIR) is on your PATH."

lint: check go-vet lint-adr-status lint-adr-numbers lint-adr-frontmatter

check:
	uvx ruff check .
	uvx ty check hack/

fmt:
	uvx ruff format .

lint-adr-status:
	@./hack/lint-adr-status

lint-adr-numbers:
	@./hack/lint-adr-numbers

lint-adr-frontmatter:
	@uv run --script ./hack/lint-adr-frontmatter

mindmap:
	@xdg-open docs/mindmap.html 2>/dev/null || open docs/mindmap.html 2>/dev/null || echo "Open docs/mindmap.html in your browser"

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

go-build:
	go build -ldflags "-X github.com/fullsend-ai/fullsend/internal/cli.version=$(VERSION)" -o bin/fullsend ./cmd/fullsend/

go-test:
	go test -race -cover ./...

go-lint:
	golangci-lint run ./...

go-fmt:
	gofmt -l -w .

go-vet:
	go vet ./...

go-tidy:
	go mod tidy

e2e-test: e2e-playwright
	go test -tags e2e -v -count=1 -timeout 4m ./e2e/admin/

e2e-playwright:
	@if [ -z "$$(ls -d $(HOME)/.cache/ms-playwright/chromium-* 2>/dev/null)" ]; then \
		echo "==> Installing Playwright Chromium..."; \
		go run github.com/playwright-community/playwright-go/cmd/playwright install chromium; \
	fi
