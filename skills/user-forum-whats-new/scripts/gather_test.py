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
    GH_TIMEOUT_SEC,
    SEARCH_LIMIT,
    build_output,
    classify,
    flatten_gh_slurp,
    in_window,
    parse_iso,
    parse_ymd,
    search_merged_at_range,
    to_z,
    today_et,
    window_bounds,
)


class TestWindowBounds(unittest.TestCase):
    def test_since_is_08_et(self):
        # 2026-08-11 is EDT (UTC-4): 08:00 ET = 12:00 UTC
        since_ts, _, _ = window_bounds(
            "2026-08-11",
            "2026-08-18",
            now=datetime(2026, 8, 18, 20, 0, tzinfo=UTC),
        )
        self.assertEqual(to_z(since_ts), "2026-08-11T12:00:00Z")

    def test_until_clamps_to_now_when_today(self):
        now = datetime(2026, 8, 18, 14, 30, tzinfo=UTC)
        _, until_ts, clamped = window_bounds("2026-08-11", "2026-08-18", now=now)
        self.assertEqual(until_ts, now)
        self.assertTrue(clamped)

    def test_until_end_of_day_et_for_past_day(self):
        # 2026-08-17 23:59:59.999999 EDT = 2026-08-18 03:59:59.999999Z
        now = datetime(2026, 8, 20, 12, 0, tzinfo=UTC)
        _, until_ts, clamped = window_bounds("2026-08-11", "2026-08-17", now=now)
        self.assertFalse(clamped)
        self.assertEqual(until_ts.year, 2026)
        self.assertEqual(until_ts.month, 8)
        self.assertEqual(until_ts.day, 18)
        self.assertEqual(until_ts.hour, 3)
        self.assertEqual(until_ts.minute, 59)

    def test_future_until_clamps_to_now(self):
        now = datetime(2026, 8, 18, 14, 30, tzinfo=UTC)
        _, until_ts, clamped = window_bounds("2026-08-11", "2026-12-31", now=now)
        self.assertEqual(until_ts, now)
        self.assertTrue(clamped)

    def test_rejects_inverted_window(self):
        with self.assertRaises(ValueError):
            window_bounds("2026-08-18", "2026-08-11")


class TestInWindow(unittest.TestCase):
    def setUp(self):
        self.since_ts, self.until_ts, _ = window_bounds(
            "2026-08-11",
            "2026-08-18",
            now=datetime(2026, 8, 18, 20, 0, tzinfo=UTC),
        )

    def test_before_08_et_excluded(self):
        # 07:00 EDT = 11:00 UTC
        self.assertFalse(in_window("2026-08-11T11:00:00Z", self.since_ts, self.until_ts))

    def test_exactly_08_et_excluded_half_open(self):
        # 08:00 EDT = 12:00 UTC == since_ts; exclusive lower bound
        self.assertFalse(in_window("2026-08-11T12:00:00Z", self.since_ts, self.until_ts))

    def test_just_after_08_et_included(self):
        self.assertTrue(in_window("2026-08-11T12:00:01Z", self.since_ts, self.until_ts))

    def test_release_afternoon_included(self):
        self.assertTrue(in_window("2026-08-11T18:26:33Z", self.since_ts, self.until_ts))

    def test_et_evening_after_utc_midnight_included_for_past_until(self):
        # Upper bound for until=2026-08-17 includes 2026-08-18T00..03:59Z
        since_ts, until_ts, _ = window_bounds(
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

    def test_prerelease_does_not_set_cutoff(self):
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
                    "body": "stable",
                    "draft": False,
                    "prerelease": False,
                },
                {
                    "tag_name": "v0.37.0-rc.1",
                    "published_at": "2026-08-12T12:00:00Z",
                    "html_url": "https://example/rc",
                    "name": "v0.37.0-rc.1",
                    "body": "rc",
                    "draft": False,
                    "prerelease": True,
                },
            ],
            "fullsend-ai/agents": [],
        }
        prs = {
            "fullsend-ai/fullsend": [
                {
                    "number": 1,
                    "title": "before stable",
                    "url": "u1",
                    "closedAt": "2026-08-11T18:00:00Z",
                    "author": {"login": "a"},
                },
                {
                    "number": 2,
                    "title": "after stable before rc",
                    "url": "u2",
                    "closedAt": "2026-08-12T06:00:00Z",
                    "author": {"login": "a"},
                },
                {
                    "number": 3,
                    "title": "after rc",
                    "url": "u3",
                    "closedAt": "2026-08-12T18:00:00Z",
                    "author": {"login": "a"},
                },
            ],
            "fullsend-ai/agents": [],
        }
        out = classify(releases, prs, since_ts, until_ts)
        tags = {r["tag"]: r["prerelease"] for r in out["releases"]}
        self.assertEqual(tags["v0.36.0"], False)
        self.assertEqual(tags["v0.37.0-rc.1"], True)
        # Cutoff stays at stable, not the later RC.
        self.assertEqual(
            out["release_cutoff_utc"]["fullsend-ai/fullsend"],
            "2026-08-11T18:26:33Z",
        )
        released_nums = {p["number"] for p in out["merged_prs"]["released"]}
        on_main_nums = {p["number"] for p in out["merged_prs"]["on_main"]}
        self.assertEqual(released_nums, {1})
        self.assertEqual(on_main_nums, {2, 3})

    def test_prerelease_only_window_has_no_cutoff(self):
        since_ts = parse_iso("2026-08-11T12:00:00Z")
        until_ts = parse_iso("2026-08-18T20:00:00Z")
        assert since_ts and until_ts
        out = classify(
            {
                "fullsend-ai/fullsend": [
                    {
                        "tag_name": "v0.37.0-rc.1",
                        "published_at": "2026-08-12T12:00:00Z",
                        "html_url": "https://example/rc",
                        "name": "rc",
                        "body": "rc",
                        "draft": False,
                        "prerelease": True,
                    }
                ],
                "fullsend-ai/agents": [],
            },
            {
                "fullsend-ai/fullsend": [
                    {
                        "number": 9,
                        "title": "x",
                        "url": "u",
                        "closedAt": "2026-08-12T06:00:00Z",
                        "author": {"login": "a"},
                    }
                ],
                "fullsend-ai/agents": [],
            },
            since_ts,
            until_ts,
        )
        self.assertEqual(len(out["releases"]), 1)
        self.assertTrue(out["releases"][0]["prerelease"])
        self.assertIsNone(out["release_cutoff_utc"])
        self.assertEqual(out["merged_prs"]["released"], [])
        self.assertEqual(len(out["merged_prs"]["on_main"]), 1)


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
            self.assertTrue(out["until_clamped"])
            self.assertEqual(out["merged_prs"]["released"], [])
            self.assertEqual(out["merged_prs"]["on_main"], [])

    def test_build_output_until_clamped_false_for_past_until(self):
        with tempfile.TemporaryDirectory() as tmp_s:
            tmp = Path(tmp_s)
            for name in (
                "rel-fullsend.json",
                "rel-agents.json",
                "prs-fullsend.json",
                "prs-agents.json",
            ):
                (tmp / name).write_text("[]", encoding="utf-8")
            out = build_output(
                "2026-08-11",
                "2026-08-17",
                tmp,
                now=datetime(2026, 8, 20, 12, 0, tzinfo=UTC),
            )
            self.assertFalse(out["until_clamped"])

    def test_build_output_fails_closed_on_bad_json(self):
        with tempfile.TemporaryDirectory() as tmp_s:
            tmp = Path(tmp_s)
            (tmp / "rel-fullsend.json").write_text("{not-json", encoding="utf-8")
            (tmp / "rel-agents.json").write_text("[]", encoding="utf-8")
            (tmp / "prs-fullsend.json").write_text("[]", encoding="utf-8")
            (tmp / "prs-agents.json").write_text("[]", encoding="utf-8")
            with self.assertRaises(RuntimeError) as ctx:
                build_output(
                    "2026-08-11",
                    "2026-08-18",
                    tmp,
                    now=datetime(2026, 8, 18, 15, 0, tzinfo=UTC),
                )
            self.assertIn("rel-fullsend.json", str(ctx.exception))

    def test_build_output_fails_closed_on_non_list(self):
        with tempfile.TemporaryDirectory() as tmp_s:
            tmp = Path(tmp_s)
            (tmp / "rel-fullsend.json").write_text('{"message":"oops"}', encoding="utf-8")
            (tmp / "rel-agents.json").write_text("[]", encoding="utf-8")
            (tmp / "prs-fullsend.json").write_text("[]", encoding="utf-8")
            (tmp / "prs-agents.json").write_text("[]", encoding="utf-8")
            with self.assertRaises(RuntimeError) as ctx:
                build_output(
                    "2026-08-11",
                    "2026-08-18",
                    tmp,
                    now=datetime(2026, 8, 18, 15, 0, tzinfo=UTC),
                )
            self.assertIn("JSON array", str(ctx.exception))


class TestHelpers(unittest.TestCase):
    def test_search_limit_constant(self):
        self.assertEqual(SEARCH_LIMIT, 1000)

    def test_gh_timeout_constant(self):
        self.assertGreaterEqual(GH_TIMEOUT_SEC, 30)

    def test_parse_ymd_rejects_week_date(self):
        with self.assertRaises(ValueError):
            parse_ymd("2026-W33-2")
        self.assertEqual(parse_ymd("2026-08-11").isoformat(), "2026-08-11")

    def test_today_et_format(self):
        self.assertRegex(today_et(), r"^\d{4}-\d{2}-\d{2}$")

    def test_to_z_uses_seconds_precision(self):
        dt = datetime(2026, 8, 18, 14, 30, 45, 123456, tzinfo=UTC)
        self.assertEqual(to_z(dt), "2026-08-18T14:30:45Z")
        self.assertNotIn(".", to_z(dt))

    def test_merged_at_range_documented_form(self):
        since = datetime(2026, 8, 11, 12, 0, 0, tzinfo=UTC)
        until = datetime(2026, 8, 18, 14, 30, 45, 987654, tzinfo=UTC)
        self.assertEqual(
            search_merged_at_range(since, until),
            "2026-08-11T12:00:00Z..2026-08-18T14:30:45Z",
        )

    def test_parse_iso_malformed_returns_none(self):
        import io
        from contextlib import redirect_stderr

        buf = io.StringIO()
        with redirect_stderr(buf):
            self.assertIsNone(parse_iso("not-a-timestamp"))
            self.assertIsNone(parse_iso("2026-13-99T99:99:99Z"))
        self.assertIn("warning: skipping malformed timestamp", buf.getvalue())

    def test_optional_bad_json_warns(self):
        import io
        from contextlib import redirect_stderr

        from gather import load_json

        with tempfile.TemporaryDirectory() as tmp_s:
            path = Path(tmp_s) / "bad.json"
            path.write_text("{not-json", encoding="utf-8")
            buf = io.StringIO()
            with redirect_stderr(buf):
                self.assertEqual(load_json(path), [])
            self.assertIn("warning: failed to parse bad.json", buf.getvalue())

    def test_required_bad_json_raises(self):
        from gather import load_json

        with tempfile.TemporaryDirectory() as tmp_s:
            path = Path(tmp_s) / "bad.json"
            path.write_text("{not-json", encoding="utf-8")
            with self.assertRaises(RuntimeError):
                load_json(path, required=True)

    def test_load_json_reads_utf8(self):
        from gather import load_json

        with tempfile.TemporaryDirectory() as tmp_s:
            path = Path(tmp_s) / "emoji.json"
            path.write_text('[{"title": "shipped 🚀"}]', encoding="utf-8")
            data = load_json(path, required=True)
            self.assertEqual(data[0]["title"], "shipped 🚀")

    def test_fetch_uses_base_main(self):
        import inspect

        from gather import fetch_into

        src = inspect.getsource(fetch_into)
        self.assertIn('"--base"', src)
        self.assertIn('"main"', src)

    def test_fetch_releases_uses_slurp(self):
        import inspect

        from gather import fetch_into

        src = inspect.getsource(fetch_into)
        self.assertIn('"--slurp"', src)
        self.assertIn('"--paginate"', src)
        self.assertIn("timeout=GH_TIMEOUT_SEC", src)

    def test_flatten_gh_slurp_pages(self):
        flat = [{"tag_name": "v1"}, {"tag_name": "v2"}]
        self.assertEqual(flatten_gh_slurp(flat), flat)
        nested = [[{"tag_name": "v1"}], [{"tag_name": "v2"}, {"tag_name": "v3"}]]
        self.assertEqual(
            flatten_gh_slurp(nested),
            [{"tag_name": "v1"}, {"tag_name": "v2"}, {"tag_name": "v3"}],
        )
        self.assertEqual(flatten_gh_slurp([]), [])


if __name__ == "__main__":
    unittest.main()
