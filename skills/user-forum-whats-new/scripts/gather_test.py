#!/usr/bin/env python3
"""Unit tests for gather.py (no network)."""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from datetime import UTC, datetime
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))

from gather import (  # noqa: E402
    SEARCH_LIMIT,
    build_output,
    classify,
    in_window,
    parse_iso,
    to_z,
    today_et,
    window_bounds,
)


class TestWindowBounds(unittest.TestCase):
    def test_since_is_08_et(self):
        # 2026-08-11 is EDT (UTC-4): 08:00 ET = 12:00 UTC
        since_ts, _ = window_bounds(
            "2026-08-11",
            "2026-08-18",
            now=datetime(2026, 8, 18, 20, 0, tzinfo=UTC),
        )
        self.assertEqual(to_z(since_ts), "2026-08-11T12:00:00Z")

    def test_until_clamps_to_now_when_today(self):
        now = datetime(2026, 8, 18, 14, 30, tzinfo=UTC)
        _, until_ts = window_bounds("2026-08-11", "2026-08-18", now=now)
        self.assertEqual(until_ts, now)

    def test_until_end_of_day_et_for_past_day(self):
        # 2026-08-17 23:59:59.999999 EDT = 2026-08-18 03:59:59.999999Z
        now = datetime(2026, 8, 20, 12, 0, tzinfo=UTC)
        _, until_ts = window_bounds("2026-08-11", "2026-08-17", now=now)
        self.assertEqual(until_ts.year, 2026)
        self.assertEqual(until_ts.month, 8)
        self.assertEqual(until_ts.day, 18)
        self.assertEqual(until_ts.hour, 3)
        self.assertEqual(until_ts.minute, 59)

    def test_rejects_inverted_window(self):
        with self.assertRaises(ValueError):
            window_bounds("2026-08-18", "2026-08-11")


class TestInWindow(unittest.TestCase):
    def setUp(self):
        self.since_ts, self.until_ts = window_bounds(
            "2026-08-11",
            "2026-08-18",
            now=datetime(2026, 8, 18, 20, 0, tzinfo=UTC),
        )

    def test_before_08_et_excluded(self):
        # 07:00 EDT = 11:00 UTC
        self.assertFalse(in_window("2026-08-11T11:00:00Z", self.since_ts, self.until_ts))

    def test_release_afternoon_included(self):
        self.assertTrue(in_window("2026-08-11T18:26:33Z", self.since_ts, self.until_ts))

    def test_et_evening_after_utc_midnight_included_for_past_until(self):
        # Upper bound for until=2026-08-17 includes 2026-08-18T00..03:59Z
        since_ts, until_ts = window_bounds(
            "2026-08-11",
            "2026-08-17",
            now=datetime(2026, 8, 20, 12, 0, tzinfo=UTC),
        )
        self.assertTrue(in_window("2026-08-18T02:00:00Z", since_ts, until_ts))
        self.assertFalse(in_window("2026-08-18T04:00:00Z", since_ts, until_ts))


class TestClassifyPerRepo(unittest.TestCase):
    def test_per_repo_cutoff(self):
        since_ts = parse_iso("2026-08-11T12:00:00Z")
        until_ts = parse_iso("2026-08-18T20:00:00Z")
        assert since_ts and until_ts
        releases = {
            "fullsend-ai/fullsend": [
                {
                    "tag_name": "v0.36.0",
                    "published_at": "2026-08-11T18:26:33Z",
                    "html_url": "https://example/fullsend",
                    "name": "v0.36.0",
                    "body": "feat",
                    "draft": False,
                }
            ],
            "fullsend-ai/agents": [
                {
                    "tag_name": "v0.36.0",
                    "published_at": "2026-08-11T18:26:52Z",
                    "html_url": "https://example/agents",
                    "name": "v0.36.0",
                    "body": "sync",
                    "draft": False,
                }
            ],
        }
        # Gap between the two tags: PR in fullsend after fullsend tag but
        # before agents tag must be on_main for fullsend, not "released"
        # via the agents cutoff.
        prs = {
            "fullsend-ai/fullsend": [
                {
                    "number": 1,
                    "title": "after fullsend tag",
                    "url": "https://example/1",
                    "closedAt": "2026-08-11T18:26:40Z",
                    "author": {"login": "bot"},
                },
                {
                    "number": 2,
                    "title": "before fullsend tag",
                    "url": "https://example/2",
                    "closedAt": "2026-08-11T18:00:00Z",
                    "author": {"login": "bot"},
                },
            ],
            "fullsend-ai/agents": [],
        }
        out = classify(releases, prs, since_ts, until_ts)
        released_nums = {p["number"] for p in out["merged_prs"]["released"]}
        on_main_nums = {p["number"] for p in out["merged_prs"]["on_main"]}
        self.assertEqual(released_nums, {2})
        self.assertEqual(on_main_nums, {1})
        self.assertEqual(
            out["release_cutoff_utc"]["fullsend-ai/fullsend"],
            "2026-08-11T18:26:33Z",
        )
        self.assertEqual(
            out["release_cutoff_utc"]["fullsend-ai/agents"],
            "2026-08-11T18:26:52Z",
        )

    def test_no_release_puts_all_on_main(self):
        since_ts = parse_iso("2026-08-11T12:00:00Z")
        until_ts = parse_iso("2026-08-18T20:00:00Z")
        assert since_ts and until_ts
        out = classify(
            {"fullsend-ai/fullsend": [], "fullsend-ai/agents": []},
            {
                "fullsend-ai/fullsend": [
                    {
                        "number": 9,
                        "title": "x",
                        "url": "u",
                        "closedAt": "2026-08-12T12:00:00Z",
                        "author": {"login": "a"},
                    }
                ],
                "fullsend-ai/agents": [],
            },
            since_ts,
            until_ts,
        )
        self.assertEqual(out["merged_prs"]["released"], [])
        self.assertEqual(len(out["merged_prs"]["on_main"]), 1)
        self.assertIsNone(out["release_cutoff_utc"])


class TestBuildOutput(unittest.TestCase):
    def test_reads_tmp_files(self):
        with tempfile.TemporaryDirectory() as tmp_s:
            tmp = Path(tmp_s)
            (tmp / "rel-fullsend.json").write_text("[]")
            (tmp / "rel-agents.json").write_text("[]")
            (tmp / "prs-fullsend.json").write_text("[]")
            (tmp / "prs-agents.json").write_text("[]")
            out = build_output(
                "2026-08-11",
                "2026-08-18",
                tmp,
                now=datetime(2026, 8, 18, 15, 0, tzinfo=UTC),
            )
            self.assertEqual(out["since"], "2026-08-11")
            self.assertEqual(out["window_start_utc"], "2026-08-11T12:00:00Z")
            self.assertEqual(out["merged_prs"]["released"], [])
            self.assertEqual(out["merged_prs"]["on_main"], [])


class TestHelpers(unittest.TestCase):
    def test_search_limit_constant(self):
        self.assertEqual(SEARCH_LIMIT, 1000)

    def test_today_et_format(self):
        self.assertRegex(today_et(), r"^\d{4}-\d{2}-\d{2}$")

    def test_bad_json_warns(self):
        import io
        from contextlib import redirect_stderr

        from gather import load_json

        with tempfile.TemporaryDirectory() as tmp_s:
            path = Path(tmp_s) / "bad.json"
            path.write_text("{not-json")
            buf = io.StringIO()
            with redirect_stderr(buf):
                self.assertEqual(load_json(path, []), [])
            self.assertIn("warning: failed to parse bad.json", buf.getvalue())


if __name__ == "__main__":
    unittest.main()
