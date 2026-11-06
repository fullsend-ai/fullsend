"""Tests for the allowlisted Semgrep ruleset drift comparison."""

from __future__ import annotations

import hashlib
import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from semgrep_ruleset_drift import compare_archives, compare_config_snapshot


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

    def compare(self, pinned: dict[str, bytes], upstream: dict[str, bytes]) -> dict[str, object]:
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

    def test_changed_pack_config_is_reported(self) -> None:
        pin_path = self.root / "config.sha256"
        config_path = self.root / "security-audit.yml"
        pinned_config = b"rules:\n- id: pinned.rule\n"
        upstream_config = b"rules:\n- id: pinned.rule\n- id: new.rule\n"
        pin_path.write_text(hashlib.sha256(pinned_config).hexdigest(), encoding="utf-8")
        config_path.write_bytes(upstream_config)

        result = compare_config_snapshot(pin_path, config_path)

        self.assertTrue(result["config_drift"])
        self.assertNotEqual(result["config_sha256_pinned"], result["config_sha256_upstream"])

    def test_matching_pack_config_is_not_reported_as_drift(self) -> None:
        pin_path = self.root / "config.sha256"
        config_path = self.root / "security-audit.yml"
        config = b"rules:\n- id: pinned.rule\n"
        pin_path.write_text(hashlib.sha256(config).hexdigest(), encoding="utf-8")
        config_path.write_bytes(config)

        self.assertFalse(compare_config_snapshot(pin_path, config_path)["config_drift"])


if __name__ == "__main__":
    unittest.main()
