#!/usr/bin/env python3
"""Tests for resolve-precommit-tools.py merge and resolution logic."""

import importlib.util
import os
import sys
import tempfile
import textwrap
import unittest

# Load the resolver module from the same directory.
_SCRIPT_DIR = os.path.dirname(__file__)
_RESOLVER = os.path.join(_SCRIPT_DIR, "resolve-precommit-tools.py")
spec = importlib.util.spec_from_file_location("resolver", _RESOLVER)
assert spec is not None and spec.loader is not None
resolver = importlib.util.module_from_spec(spec)
sys.modules["resolver"] = resolver
spec.loader.exec_module(resolver)


def _write_yaml(content: str) -> str:
    """Write YAML content to a temp file and return its path."""
    fd, path = tempfile.mkstemp(suffix=".yaml")
    with os.fdopen(fd, "w") as f:
        f.write(textwrap.dedent(content))
    return path


# ---------------------------------------------------------------------------
# merge_registries tests
# ---------------------------------------------------------------------------


class TestMergeRegistries(unittest.TestCase):
    def test_merge_new_entry(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        local = {
            "tools": [
                {"hook_id": "fmt", "repo": "local", "install": {"name": "formatter"}},
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 2)
        self.assertEqual(merged["tools"][0]["hook_id"], "lint")
        self.assertEqual(merged["tools"][1]["hook_id"], "fmt")
        self.assertEqual(warnings, [])

    def test_merge_override(self):
        upstream = {
            "tools": [
                {
                    "hook_id": "lint",
                    "repo": "https://github.com/example/lint",
                    "install": {"name": "linter", "version": "1.0"},
                },
            ]
        }
        local = {
            "tools": [
                {
                    "hook_id": "lint",
                    "repo": "https://github.com/example/lint",
                    "install": {"name": "linter", "version": "2.0"},
                },
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertEqual(merged["tools"][0]["install"]["version"], "2.0")
        self.assertEqual(warnings, [])

    def test_merge_exclude(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
                {"hook_id": "fmt", "repo": "local", "install": {"name": "formatter"}},
            ]
        }
        local = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "exclude": True},
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertEqual(merged["tools"][0]["hook_id"], "fmt")
        self.assertEqual(warnings, [])

    def test_merge_empty_local(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        local = {"tools": []}
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertEqual(warnings, [])

    def test_merge_preserves_order(self):
        upstream = {
            "tools": [
                {"hook_id": "a", "repo": "local", "install": {"name": "tool-a"}},
                {"hook_id": "b", "repo": "local", "install": {"name": "tool-b"}},
                {"hook_id": "c", "repo": "local", "install": {"name": "tool-c"}},
            ]
        }
        local = {
            "tools": [
                {"hook_id": "d", "repo": "local", "install": {"name": "tool-d"}},
            ]
        }
        merged, _ = resolver.merge_registries(upstream, local)
        ids = [t["hook_id"] for t in merged["tools"]]
        self.assertEqual(ids, ["a", "b", "c", "d"])

    def test_merge_malformed_local_missing_tools(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        local = {"not_tools": []}
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertTrue(any("invalid" in w for w in warnings))

    def test_merge_malformed_local_not_dict(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, "not a dict")
        self.assertEqual(len(merged["tools"]), 1)
        self.assertTrue(any("invalid" in w for w in warnings))

    def test_merge_entry_missing_hook_id(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        local = {
            "tools": [
                {"repo": "local", "install": {"name": "orphan"}},
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertTrue(any("missing hook_id" in w for w in warnings))

    def test_same_hook_id_different_repo(self):
        """Same hook_id but different repos should coexist, not override."""
        upstream = {
            "tools": [
                {
                    "hook_id": "lint",
                    "repo": "https://github.com/org-a/lint",
                    "install": {"name": "lint-a"},
                },
            ]
        }
        local = {
            "tools": [
                {
                    "hook_id": "lint",
                    "repo": "https://github.com/org-b/lint",
                    "install": {"name": "lint-b"},
                },
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 2)
        names = [t["install"]["name"] for t in merged["tools"]]
        self.assertIn("lint-a", names)
        self.assertIn("lint-b", names)
        self.assertEqual(warnings, [])

    def test_match_entry_collision_warns(self):
        """Local entry with same match_entry as a different upstream entry emits a warning."""
        upstream = {
            "tools": [
                {
                    "hook_id": "uv-run",
                    "repo": "local",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
            ]
        }
        local = {
            "tools": [
                {
                    "hook_id": "custom-uv",
                    "repo": "https://example.com/custom",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "custom-uv", "version": "1.0"},
                },
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 2)
        self.assertTrue(any("collides" in w and "match_entry" in w for w in warnings))

    def test_match_entry_same_key_no_collision(self):
        """Override of the same (repo, hook_id) should not warn on match_entry."""
        upstream = {
            "tools": [
                {
                    "hook_id": "uv-run",
                    "repo": "local",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
            ]
        }
        local = {
            "tools": [
                {
                    "hook_id": "uv-run",
                    "repo": "local",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "uv", "version": "0.12.0"},
                },
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertFalse(any("collides" in w for w in warnings))

    def test_exclude_nonexistent_entry(self):
        upstream = {
            "tools": [
                {"hook_id": "lint", "repo": "local", "install": {"name": "linter"}},
            ]
        }
        local = {
            "tools": [
                {"hook_id": "ghost", "repo": "local", "exclude": True},
            ]
        }
        merged, warnings = resolver.merge_registries(upstream, local)
        self.assertEqual(len(merged["tools"]), 1)
        self.assertEqual(warnings, [])


# ---------------------------------------------------------------------------
# resolve tests (with parsed dict registry)
# ---------------------------------------------------------------------------


class TestResolve(unittest.TestCase):
    def _precommit_file(self, content: str) -> str:
        path = _write_yaml(content)
        self.addCleanup(lambda p=path: os.unlink(p) if os.path.exists(p) else None)
        return path

    def test_resolve_uv_match(self):
        """Hooks with entry: 'uv run ...' should match the uv match_entry."""
        precommit = self._precommit_file("""\
            repos:
              - repo: local
                hooks:
                  - id: mypy-check
                    entry: "uv run mypy"
                    language: system
        """)
        registry = {
            "tools": [
                {
                    "hook_id": "uv-run",
                    "repo": "local",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
            ]
        }
        result = resolver.resolve(precommit, registry)
        self.assertEqual(len(result["tools"]), 1)
        self.assertEqual(result["tools"][0]["name"], "uv")

    def test_resolve_uvx_match(self):
        """Hooks with entry: 'uvx ...' should match the uvx match_entry."""
        precommit = self._precommit_file("""\
            repos:
              - repo: local
                hooks:
                  - id: ty
                    entry: "uvx ty check"
                    language: system
        """)
        registry = {
            "tools": [
                {
                    "hook_id": "ty",
                    "repo": "local",
                    "match_entry": "uvx",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
            ]
        }
        result = resolver.resolve(precommit, registry)
        self.assertEqual(len(result["tools"]), 1)
        self.assertEqual(result["tools"][0]["name"], "uv")

    def test_resolve_dedup(self):
        """Both uv and uvx hooks resolve to one install via seen_names dedup."""
        precommit = self._precommit_file("""\
            repos:
              - repo: local
                hooks:
                  - id: ty
                    entry: "uvx ty check"
                    language: system
                  - id: mypy-check
                    entry: "uv run mypy"
                    language: system
        """)
        registry = {
            "tools": [
                {
                    "hook_id": "ty",
                    "repo": "local",
                    "match_entry": "uvx",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
                {
                    "hook_id": "uv-run",
                    "repo": "local",
                    "match_entry": "uv",
                    "install": {"type": "binary", "name": "uv", "version": "0.11.14"},
                },
            ]
        }
        result = resolver.resolve(precommit, registry)
        self.assertEqual(len(result["tools"]), 1)
        self.assertEqual(result["tools"][0]["name"], "uv")

    def test_resolve_with_merged_registry(self):
        """End-to-end: upstream + local merged, then resolved."""
        upstream = {
            "tools": [
                {
                    "hook_id": "lint",
                    "repo": "local",
                    "match_entry": "lychee",
                    "install": {"type": "binary", "name": "lychee", "version": "0.24.2"},
                },
            ]
        }
        local = {
            "tools": [
                {
                    "hook_id": "fmt",
                    "repo": "local",
                    "match_entry": "myfmt",
                    "install": {"type": "binary", "name": "myfmt", "version": "1.0"},
                },
            ]
        }
        merged, _ = resolver.merge_registries(upstream, local)

        precommit = self._precommit_file("""\
            repos:
              - repo: local
                hooks:
                  - id: check-links
                    entry: "lychee ."
                    language: system
                  - id: format-code
                    entry: "myfmt --fix"
                    language: system
        """)
        result = resolver.resolve(precommit, merged)
        os.unlink(precommit)
        names = [t["name"] for t in result["tools"]]
        self.assertIn("lychee", names)
        self.assertIn("myfmt", names)
        self.assertEqual(len(result["tools"]), 2)


if __name__ == "__main__":
    unittest.main()
