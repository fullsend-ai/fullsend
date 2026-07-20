.DEFAULT_GOAL := help
.PHONY: help bootstrap ensure-hooks lint lint-all check fmt \
       mindmap go-build go-test go-lint go-fmt go-vet go-tidy \
       lint-md-links script-test test \
       e2e-test behaviour-test lint-eval-cases functional-tests \
       wasm-build

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
	@echo "  script-test          - Run shell script tests (post-triage, post-code, post-review, pre-fetch-prior-review, reconcile-repos, validate-output-schema)"
	@echo "  test                 - Run all checks: lint-all, go-test, script-test, lint-eval-cases"
	@echo "  e2e-test             - Run admin e2e tests (CI: OIDC mint; local: gh auth login or GH_TOKEN)"
	@echo "  behaviour-test       - Run Gherkin behaviour tests (installs fullsend per-repo; CI: OIDC mint)"
	@echo "  lint-eval-cases      - Lint eval case definitions (annotations.yaml completeness)"
	@echo "  functional-tests     - Run functional agent tests (requires EVAL_ORG, FULLSEND_DIR, GH_TOKEN, GCP creds)"
	@echo "  wasm-build           - Verify mintcore WASM build (GOOS=js GOARCH=wasm) and report gzip size"

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
	@echo "==> Installing pinact (GitHub Actions SHA-pin checker)..."
	curl -sSfL "https://github.com/suzuki-shunsuke/pinact/releases/download/v4.1.0/pinact_linux_amd64.tar.gz" -o /tmp/pinact.tar.gz
	echo "8fcbf1b3e95551c82fd995535e3c1defa70e23299ce36eb3afd6c98778de6ca0  /tmp/pinact.tar.gz" | sha256sum -c
	tar xzf /tmp/pinact.tar.gz -C "$(BOOTSTRAP_BIN_DIR)" pinact
	@echo "==> Installing pre-commit hooks..."
	PATH="$(BOOTSTRAP_BIN_DIR):$(PATH)" pre-commit install
	@echo ""
	@echo "==> Bootstrap complete!"
	@echo "    Make sure $(BOOTSTRAP_BIN_DIR) is on your PATH."

ensure-hooks:
	@if [ -z "$$CI" ] && [ -z "$$(git config --get core.hooksPath 2>/dev/null)" ]; then \
		hooks_dir=$$(git rev-parse --git-path hooks 2>/dev/null); \
		if [ -n "$$hooks_dir" ] && [ ! -f "$$hooks_dir/pre-commit" ]; then \
			echo "==> Installing pre-commit hooks..."; \
			pre-commit install; \
		fi; \
	fi

lint: ensure-hooks
	pre-commit run

lint-all: ensure-hooks
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
	GH_TOKEN= GITHUB_TOKEN= \
	GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=false \
	go test -race -cover ./...

go-lint:
	golangci-lint run ./...

go-fmt:
	gofmt -l -w .

go-vet:
	go vet ./...

go-tidy:
	go mod tidy

wasm-build:
	@echo "==> Verifying mintcore WASM build (GOOS=js GOARCH=wasm)..."
	cd internal/mintcore && GOOS=js GOARCH=wasm go build ./...
	@echo "==> WASM build OK"

lint-md-links:
	lychee --offline --no-progress --include-fragments --exclude-path node_modules --exclude-path experiments '**/*.md'

define run-timed
	@start=$$(date +%s); \
	rc=0; $(1) || rc=$$?; \
	elapsed=$$(($$(date +%s) - $$start)); \
	printf '::debug::script-test timing: %s completed in %ds\n' '$(1)' "$$elapsed"; \
	exit $$rc
endef

script-test:
	$(call run-timed,bash scripts/check-e2e-authorization-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/post-triage-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/post-prioritize-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/post-code-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/post-review-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/reconcile-repos-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/validate-output-schema-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/pre-code-test.sh)
	$(call run-timed,bash internal/scaffold/fullsend-repo/scripts/pre-fetch-prior-review-test.sh)
	$(call run-timed,python3 internal/scaffold/fullsend-repo/scripts/process-fix-result-test.py)
	$(call run-timed,python3 skills/topissues/scripts/topissues_test.py)
	$(call run-timed,python3 -m pytest gitlint_rules_test.py -v)

test: lint-all go-test script-test lint-eval-cases

e2e-test:
	go test -tags e2e -v -count=1 -timeout 30m ./e2e/admin/

behaviour-test:
	go test -tags behaviour -v -count=1 -timeout 30m ./e2e/behaviour/

# Functional agent evals — run agents against ephemeral GitHub repos and judge results.
# Required env: EVAL_ORG (GitHub org for ephemeral repos), plus GCP creds for Vertex AI.
# GH_TOKEN defaults to `gh auth token` if not set.
FULLSEND_DIR ?= $(CURDIR)/internal/scaffold/fullsend-repo
EVAL_AGENTS  ?= triage

lint-eval-cases:
	@for agent in $(EVAL_AGENTS); do \
		./eval/lint-cases.sh "$$agent"; \
	done

functional-tests: lint-eval-cases
	@for agent in $(EVAL_AGENTS); do \
		FULLSEND_DIR="$(FULLSEND_DIR)" ./eval/run-functional.sh "$$agent"; \
	done
