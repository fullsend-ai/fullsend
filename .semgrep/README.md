# Semgrep configuration

`scripts/semgrep-scan` downloads the Semgrep Registry's `p/security-audit`
configuration from the URL in `scripts/semgrep-ruleset-url` and verifies it
against the SHA-256 in `scripts/semgrep-ruleset.sha256`. CI fails closed if it
cannot download the pinned ruleset. Local runs reuse a verified cache keyed by
the SHA-256; if a cache miss cannot fetch the pinned bytes, the hook warns and
skips the scan. Use `SKIP=semgrep` with pre-commit or `make test` to skip the
hook explicitly while offline.

The scheduled `Semgrep Ruleset Drift` workflow compares the live response with
the pin and opens or updates an issue when it changes. To update the ruleset,
review the new configuration and scan results, update the pin, and fix new
findings or add reviewed `nosemgrep: <rule-id>` comments with their rationale.
There is no separate baseline file. The rules are fetched at runtime and are
not stored in this repository.

The pre-commit hook scans changed files of all types, except paths matched by
its `exclude:` expression in `.pre-commit-config.yaml`. Semgrep's directory
scan in CI honors `.semgrepignore`; explicit paths passed to Semgrep bypass
that file, so keep the pre-commit expression aligned with `.semgrepignore`.
Both exclude Go test files, `test/` and `tests/` directories, `.semgrep/`, and
synthetic behaviour fixtures and captured results. CI uploads the SARIF report
as a run artifact for 30 days.
