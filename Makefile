.DEFAULT_GOAL := help
.PHONY: help lint fmt lint-adr-status lint-adr-numbers lint-adr-frontmatter evals test test-evals

help:
	@echo "Available targets:"
	@echo "  help                 - Show this help message"
	@echo "  lint                 - Run all linting and validation"
	@echo "  fmt                  - Format Python code with ruff"
	@echo "  lint-adr-status      - Validate ADR statuses in all ADR files"
	@echo "  lint-adr-numbers     - Check for duplicate ADR numeric identifiers"
	@echo "  lint-adr-frontmatter - Validate ADR frontmatter and cross-references"
	@echo "  evals                - Run skill evals (SKILL=name for one skill)"
	@echo "  test                 - Run all tests"
	@echo "  test-evals           - Run eval framework unit tests"

lint: lint-pre-commit lint-adr-status lint-adr-numbers lint-adr-frontmatter

lint-pre-commit:
	pre-commit run --all-files

fmt:
	uvx ruff format .

lint-adr-status:
	@./hack/lint-adr-status

lint-adr-numbers:
	@./hack/lint-adr-numbers

lint-adr-frontmatter:
	@uv run --script ./hack/lint-adr-frontmatter

evals:
ifdef SKILL
	@uv run --script ./hack/evals/run.py $(SKILL)
else
	@uv run --script ./hack/evals/run.py
endif

test: test-evals

test-evals:
	@cd hack/evals && uv run --script test_evals.py
