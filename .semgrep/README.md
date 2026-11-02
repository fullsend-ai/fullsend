# Semgrep configuration

`scripts/semgrep-scan` downloads the Semgrep Registry's `p/security-audit`
configuration from `https://semgrep.dev/c/p/security-audit` into a temporary
directory. It verifies the downloaded bytes against the SHA-256 pin in the
script before scanning. The pin prevents unreviewed registry changes from
silently changing local or CI results; a changed response fails closed.

To update the ruleset, fetch the URL over HTTPS, review the configuration and
new findings, then update the SHA-256 pin and accepted baseline in the same
change. The rules are fetched at runtime and are not stored in this repository.

The pre-commit hook scans changed files of all types. CI scans the repository
in a separate job, and uploads the SARIF report as a run artifact for 30 days.
Both use `.semgrepignore` to exclude Go test files, `test/` and `tests/`
directories, the `.semgrep/` configuration directory, and synthetic behaviour
fixtures and captured results.
