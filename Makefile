.PHONY: help lint check fmt lint-adr-status lint-adr-numbers lint-adr-frontmatter
.DEFAULT_GOAL := help

help:
	@echo "Available targets:"
	@echo "  help                 - Show this help message"
	@echo "  lint                 - Run all linting and validation"
	@echo "  check                - Run ruff and ty checks on Python"
	@echo "  fmt                  - Format Python code with ruff"
	@echo "  lint-adr-status      - Validate ADR statuses in all ADR files"
	@echo "  lint-adr-numbers     - Check for duplicate ADR numeric identifiers"
	@echo "  lint-adr-frontmatter - Validate ADR frontmatter and cross-references"

lint: check lint-adr-status lint-adr-numbers lint-adr-frontmatter

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
