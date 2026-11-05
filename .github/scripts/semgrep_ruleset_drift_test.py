"""Tests for the allowlisted Semgrep ruleset drift comparison."""

from __future__ import annotations

import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from semgrep_ruleset_drift import compare_archives


class CompareArchivesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.rule_path = "go/lang/security/audit/example.yaml"

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def write_archive(self, path: Path, files: dict[str, bytes]) -> None:
        with tarfile.open(path, "w:gz") as archive:
            for name, content in files.items():
                entry = tarfile.TarInfo(f"semgrep-rules-test/{name}")
                entry.size = len(content)
                archive.addfile(entry, io.BytesIO(content))

    def compare(self, pinned: dict[str, bytes], upstream: dict[str, bytes]) -> dict[str, list[str]]:
        pinned_path = self.root / "pinned.tar.gz"
        upstream_path = self.root / "upstream.tar.gz"
        self.write_archive(pinned_path, pinned)
        self.write_archive(upstream_path, upstream)
        return compare_archives(pinned_path, upstream_path, [self.rule_path])

    def test_unrelated_file_changes_do_not_report_rule_drift(self) -> None:
        result = self.compare(
            {
                self.rule_path: b"same rule",
                "go/lang/security/other.yaml": b"old unrelated rule",
            },
            {
                self.rule_path: b"same rule",
                "go/lang/security/other.yaml": b"new unrelated rule",
            },
        )
        self.assertEqual(result["changed_files"], [])
        self.assertEqual(result["missing_from_pinned"], [])
        self.assertEqual(result["missing_from_upstream"], [])

    def test_allowlisted_rule_changes_are_reported(self) -> None:
        result = self.compare(
            {self.rule_path: b"old rule"},
            {self.rule_path: b"new rule"},
        )
        self.assertEqual(result["changed_files"], [self.rule_path])

    def test_removed_allowlisted_rule_is_reported(self) -> None:
        result = self.compare({self.rule_path: b"rule"}, {})
        self.assertEqual(result["missing_from_upstream"], [self.rule_path])


if __name__ == "__main__":
    unittest.main()
