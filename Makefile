.DEFAULT_GOAL := help
.PHONY: help bootstrap lint lint-all check fmt \
       mindmap go-build go-test go-lint go-fmt go-vet go-tidy \
       lint-md-links script-test test \
       e2e-test e2e-playwright e2e-export-session e2e-upload-session

# Let Go automatically download the toolchain version required by go.mod.
# This ensures local builds use the right version without manual intervention.
# goreleaser is unaffected because it does not invoke Makefile targets.
export GOTOOLCHAIN := auto

help:
	@echo "Available targets:"
	@echo "  help                 - Show this help message"
	@echo "  bootstrap            - Install all development tools"
	@echo "  lint                 - Run linting on staged changes"
	@echo "  lint-all             - Run linting on all files"
	@echo "  check                - Run ruff and ty checks on Python"
	@echo "  fmt                  - Format Python code with ruff"
	@echo "  mindmap              - Open the interactive document graph in a browser"
	@echo "  go-build             - Build the fullsend binary"
	@echo "  go-test              - Run Go tests with race detection and coverage"
	@echo "  go-lint              - Run golangci-lint"
	@echo "  go-fmt               - Format Go code"
	@echo "  go-vet               - Run go vet"
	@echo "  go-tidy              - Run go mod tidy"
	@echo "  lint-md-links        - Check markdown files for broken in-repo links and anchors"
	@echo "  script-test          - Run shell script tests (post-triage, post-code, post-review, reconcile-repos, validate-output-schema)"
	@echo "  test                 - Run all checks: lint, go-vet, go-test, script-test"
	@echo "  e2e-test             - Run admin e2e tests (requires E2E_GITHUB_SESSION_FILE or E2E_GITHUB_USERNAME + E2E_GITHUB_PASSWORD)"
	@echo "  e2e-export-session   - Login to GitHub and export a Playwright session file"
	@echo "  e2e-upload-session   - Export session and upload it as a GitHub repo secret"

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
	@echo "==> Installing lychee (markdown link checker)..."
	curl -sSfL "https://github.com/lycheeverse/lychee/releases/download/lychee-v0.24.2/lychee-x86_64-unknown-linux-gnu.tar.gz" -o /tmp/lychee.tar.gz
	echo "1f4e0ef7f6554a6ed33dd7ac144fb2e1bbed98598e7af973042fc5cd43951c9a  /tmp/lychee.tar.gz" | sha256sum -c
	tar xzf /tmp/lychee.tar.gz -C "$(BOOTSTRAP_BIN_DIR)" --strip-components=1 lychee-x86_64-unknown-linux-gnu/lychee
	@echo "==> Installing pre-commit hooks..."
	PATH="$(BOOTSTRAP_BIN_DIR):$(PATH)" pre-commit install
	@echo ""
	@echo "==> Bootstrap complete!"
	@echo "    Make sure $(BOOTSTRAP_BIN_DIR) is on your PATH."

lint:
	pre-commit run

lint-all:
	pre-commit run --all-files

check:
	uvx ruff check .
	uvx ty check hack/

fmt:
	uvx ruff format .

mindmap:
	@xdg-open web/public/index.html 2>/dev/null || open web/public/index.html 2>/dev/null || echo "Open web/public/index.html in your browser"

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

lint-md-links:
	lychee --offline --no-progress --include-fragments --exclude-path node_modules --exclude-path experiments '**/*.md'

script-test:
	bash internal/scaffold/fullsend-repo/scripts/post-triage-test.sh
	bash internal/scaffold/fullsend-repo/scripts/post-code-test.sh
	bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh
	bash internal/scaffold/fullsend-repo/scripts/reconcile-repos-test.sh
	bash internal/scaffold/fullsend-repo/scripts/validate-output-schema-test.sh
	bash internal/scaffold/fullsend-repo/scripts/pre-code-test.sh
	python3 internal/scaffold/fullsend-repo/scripts/process-fix-result-test.py

test: lint go-vet go-test script-test

E2E_SESSION_FILE ?= $(CURDIR)/.playwright/session.json

e2e-test: e2e-playwright
	@if [ -n "$$E2E_GITHUB_PASSWORD_FILE" ] && [ -z "$$E2E_GITHUB_PASSWORD" ]; then \
		export E2E_GITHUB_PASSWORD="$$(cat "$$E2E_GITHUB_PASSWORD_FILE")"; \
	fi; \
	if [ -z "$$E2E_GITHUB_SESSION_FILE" ] && [ -n "$$E2E_GITHUB_USERNAME" ] && [ -n "$$E2E_GITHUB_PASSWORD" ]; then \
		echo "==> No session file set, generating one from credentials..."; \
		$(MAKE) e2e-export-session; \
		export E2E_GITHUB_SESSION_FILE="$(E2E_SESSION_FILE)"; \
	fi; \
	go test -tags e2e -v -count=1 -timeout 30m ./e2e/admin/

e2e-export-session: e2e-playwright
	@if [ -n "$$E2E_GITHUB_PASSWORD_FILE" ] && [ -z "$$E2E_GITHUB_PASSWORD" ]; then \
		export E2E_GITHUB_PASSWORD="$$(cat "$$E2E_GITHUB_PASSWORD_FILE")"; \
	fi; \
	E2E_GITHUB_SESSION_FILE="$(E2E_SESSION_FILE)" go run ./e2e/cmd/export-session/

e2e-upload-session: e2e-export-session
	@echo "==> Uploading session to GitHub repo secret..."
	base64 -w0 "$(E2E_SESSION_FILE)" | gh secret set E2E_GITHUB_SESSION
	@echo "==> Done. Session uploaded as E2E_GITHUB_SESSION."

e2e-playwright:
	@if [ -z "$$(ls -d $(HOME)/.cache/ms-playwright/chromium-* 2>/dev/null)" ]; then \
		echo "==> Installing Playwright Chromium..."; \
		go run github.com/playwright-community/playwright-go/cmd/playwright install chromium; \
	fi
