"""Gitlint rule that forbids misleading type+scope combinations.

Types like ``feat`` and ``fix`` appear in user-facing release notes.
Scopes like ``ci`` and ``e2e`` describe infrastructure, not user-visible
changes.  Using them together pollutes the release notes with entries
that mean nothing to end users.
"""

import re

from gitlint.rules import CommitRule, RuleViolation

# Pattern: type(scope): description
_CONVENTIONAL = re.compile(r"^(?P<type>\w+)\((?P<scope>[^)]+)\)")

# Map of (type, scope) -> suggested replacement.
# Scope matches are case-insensitive.
_FORBIDDEN = {
    ("feat", "ci"): "ci(<subsystem>)",
    ("fix", "ci"): "ci(<subsystem>)",
    ("feat", "e2e"): "ci(e2e)",
    ("fix", "e2e"): "ci(e2e)",
}


class ForbiddenTypeScope(CommitRule):
    name = "forbidden-type-scope"
    id = "UC1"

    def validate(self, commit):
        title = commit.message.title
        m = _CONVENTIONAL.match(title)
        if not m:
            return []

        ctype = m.group("type").lower()
        scope = m.group("scope").lower()
        key = (ctype, scope)

        if key not in _FORBIDDEN:
            return []

        suggestion = _FORBIDDEN[key]
        return [
            RuleViolation(
                self.id,
                f'"{ctype}({scope})" pollutes release notes with non-user-facing changes. '
                f'Use "{suggestion}: ..." instead.',
                line_nr=1,
            )
        ]
