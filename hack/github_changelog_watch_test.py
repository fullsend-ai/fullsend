"""Tests for hack/github-changelog-watch.

Sources and GitHub are served from the recorded fixtures in
hack/testdata/github-changelog-watch/ through a fake fetch function; no test
touches the network.
"""

import copy
import importlib.machinery
import importlib.util
import json
import re
import sys
from datetime import UTC, datetime
from pathlib import Path
from urllib.parse import parse_qs, urlsplit

import pytest

REPO_ROOT = Path(__file__).resolve().parent.parent
SCRIPT = REPO_ROOT / "hack" / "github-changelog-watch"
FIXTURES = REPO_ROOT / "hack" / "testdata" / "github-changelog-watch"
REPO = "fullsend-ai/fullsend"


def load_watcher():
    loader = importlib.machinery.SourceFileLoader("github_changelog_watch", str(SCRIPT))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    assert spec is not None
    module = importlib.util.module_from_spec(spec)
    sys.modules[loader.name] = module
    loader.exec_module(module)
    return module


W = load_watcher()
NOW = datetime(2026, 11, 9, 12, 0, tzinfo=UTC)  # ISO week 2026-W46


def fixture(name):
    text = (FIXTURES / name).read_text()
    return text if name.endswith(".xml") else json.loads(text)


PUBLISHED = fixture("changelog-98897-published-2026-09-17.json")
EDITED = fixture("changelog-98897-edited-2026-10-21.json")
LABELS = fixture("changelogs-labels-2026-08-01-to-2026-11-09.json")
DEPRECATIONS = fixture("changelogs-deprecations-2026-10-25-to-2026-11-09.json")
RUNNER_IMAGES = fixture("runner-images-announcements-2026-11-09.json")
RSS_ACTIONS = fixture("rss-label-actions-2026-11-09.xml")


class FakeGitHub:
    """Serves the changelog, RSS and runner-images sources and a minimal issues API."""

    def __init__(
        self,
        labels=None,
        deprecations=None,
        runner_images=None,
        rss=None,
        rest_body=None,
        issues=None,
        wp_pages=1,
        fail_runner_images=False,
    ):
        self.labels = labels or []
        self.deprecations = deprecations or []
        self.runner_images = runner_images or []
        self.rss = rss or {}
        self.rest_body = rest_body
        self.wp_pages = wp_pages
        self.fail_runner_images = fail_runner_images
        self.issues = [copy.deepcopy(i) for i in issues or []]
        self.comments = {}
        self.writes = []
        self.requests = []

    def add_issue(self, title, body, labels, state="open"):
        number = 100 + len(self.issues)
        issue = {
            "number": number,
            "title": title,
            "body": body,
            "state": state,
            "labels": [{"name": n} for n in labels],
            "user": {"login": "github-actions[bot]"},
        }
        self.issues.append(issue)
        return issue

    def __call__(self, req):
        self.requests.append(req)
        target = req.target
        if target.startswith(W.CHANGELOG_API):
            if self.rest_body is not None:
                return W.Response(200, self.rest_body, {"content-type": "text/html"})
            q = parse_qs(urlsplit(target).query)
            posts = self.deprecations if "type" in q else self.labels
            page = int(q["page"][0])
            chunk = -(-len(posts) // self.wp_pages) if posts else 1
            body = json.dumps(posts[(page - 1) * chunk : page * chunk])
            return W.Response(200, body, {"x-wp-totalpages": str(self.wp_pages)})
        if target.startswith("https://"):
            body = self.rss.get(target)
            return W.Response(200, body) if body else W.Response(404)
        if target.startswith(W.RUNNER_IMAGES_ISSUES):
            if self.fail_runner_images:
                return W.Response(502, "bad gateway")
            return W.Response(200, json.dumps(self.runner_images))
        return self.issues_api(req)

    def issues_api(self, req):
        path, _, query = req.target.partition("?")
        base = f"repos/{REPO}/"
        assert path.startswith(base), req.target
        rest = path[len(base) :]
        if req.method != "GET":
            self.writes.append((req.method, rest, req.data))
        if rest == "labels" and req.method == "POST":
            return W.Response(201, json.dumps(req.data))
        if rest == "issues" and req.method == "GET":
            q = parse_qs(query)
            if "creator" in q:
                login = q["creator"][0]
                found = [i for i in self.issues if i.get("user", {}).get("login") == login]
            else:
                label = q["labels"][0]
                found = [i for i in self.issues if label in {lb["name"] for lb in i["labels"]}]
            return W.Response(200, json.dumps(found))
        if rest == "issues" and req.method == "POST":
            issue = self.add_issue(req.data["title"], req.data["body"], req.data["labels"])
            return W.Response(201, json.dumps(issue))
        m = re.fullmatch(r"issues/(\d+)(/comments)?", rest)
        assert m, req.target
        number = int(m.group(1))
        issue = next(i for i in self.issues if i["number"] == number)
        if m.group(2) and req.method == "GET":
            return W.Response(200, json.dumps(self.comments.get(number, [])))
        if m.group(2) and req.method == "POST":
            self.comments.setdefault(number, []).append({"body": req.data["body"]})
            return W.Response(201, json.dumps({"body": req.data["body"]}))
        if req.method == "PATCH":
            issue.update(req.data)
            return W.Response(200, json.dumps(issue))
        raise AssertionError(f"unexpected request {req}")

    def issue_writes(self):
        return [w for w in self.writes if w[1] != "labels"]

    def opened(self):
        return [w for w in self.writes if w[:2] == ("POST", "issues")]


def group1(pattern, text):
    m = re.search(pattern, text)
    assert m, pattern
    return m.group(1)


def run(fake, *args, now=NOW):
    return W.main(["--repo", REPO, *args], fetch=fake, now=now)


def actions(out):
    return [line for line in out.splitlines() if not line.startswith("::")]


def tracked_issue(post_id):
    body = f"tracked\n\n<!-- platform-change:source=changelog:id={post_id} -->\n"
    return {
        "number": 7,
        "title": "[platform-change] tracked",
        "body": body,
        "state": "open",
        "labels": [{"name": "platform-change"}],
    }


def test_published_post_opens_one_tier1_issue(capsys):
    fake = FakeGitHub(labels=PUBLISHED)
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0

    opened = fake.opened()
    assert len(opened) == 1
    data = opened[0][2]
    assert data["title"] == (
        "[platform-change] Workflow execution protections in GitHub Actions generally available"
    )
    assert data["labels"] == ["platform-change"]
    body = data["body"]
    matched = group1(r"\*\*Matched:\*\* (.*)", body).split(", ")
    assert {"pull_request_target", "default rule", "evaluate mode"} <= set(matched)
    assert "<!-- platform-change:source=changelog:id=98897 -->" in body
    assert PUBLISHED[0]["link"] in body
    assert "**Published:** 2026-09-17T08:45:26Z" in body
    assert "Does this affect fullsend's shim, reusable workflows, actions or consumers?" in body
    quoted = [ln for ln in body.splitlines() if ln.startswith("> ")]
    assert 1 <= len(quoted) <= 5
    assert any("November 2, 2026" in q for q in quoted)
    # Both labels are created before the first write.
    assert [w[2]["name"] for w in fake.writes if w[1] == "labels"] == [
        "platform-change",
        "platform-change-digest",
    ]
    out = actions(capsys.readouterr().out)
    assert out[0].startswith("OPEN changelog:98897 ")
    assert out[-1] == ("SUMMARY open=1 comment=0 digest=0 skip-dup=0 ignore-cosmetic=0")


def test_edit_of_known_post_comments_once(capsys):
    fake = FakeGitHub(labels=EDITED, issues=[tracked_issue(98897)])
    assert run(fake, "--since", "2026-10-20") == 0

    assert fake.issue_writes() == [
        ("POST", "issues/7/comments", fake.issue_writes()[0][2]),
    ]
    body = fake.comments[7][0]["body"]
    assert "Editor's note (October 21, 2026): We updated the automatic enforcement date" in body
    assert "2026-10-21T09:18:27Z" in body
    assert re.search(r"<!-- platform-change:id=98897:note=[0-9a-f]{12} -->", body)
    first = actions(capsys.readouterr().out)[0]
    assert first.startswith("COMMENT changelog:98897 ") and " #7 modified " in first

    assert run(fake, "--since", "2026-10-20") == 0
    assert len(fake.issue_writes()) == 1
    out = actions(capsys.readouterr().out)
    assert out[0].startswith("SKIP-DUP changelog:98897 ") and " #7 edit " in out[0]
    assert out[-1].startswith("SUMMARY open=0 comment=0 ")


def test_edit_without_issue_opens_one(capsys):
    fake = FakeGitHub(labels=EDITED)
    assert run(fake, "--since", "2026-10-20") == 0

    opened = fake.opened()
    assert len(opened) == 1
    body = opened[0][2]["body"]
    assert "editor's note (October 21, 2026)" in body
    assert "<!-- platform-change:source=changelog:id=98897 -->" in body
    assert any("Editor’s note (October 21, 2026)" in ln for ln in body.splitlines())

    # The issue body carries the edit marker, so a rerun does not comment.
    assert run(fake, "--since", "2026-10-20") == 0
    assert len(fake.issue_writes()) == 1
    assert actions(capsys.readouterr().out)[-2].startswith("SKIP-DUP changelog:98897 ")


@pytest.mark.parametrize(
    "post, since",
    [
        # No editor's note: the published copy, re-touched later.
        ({**PUBLISHED[0], "modified": "2026-11-01T10:00:00"}, "2026-10-25"),
        # Editor's note dated October 21, before since.
        ({**EDITED[0], "modified": "2026-11-01T10:00:00"}, "2026-10-25"),
    ],
)
def test_cosmetic_edit_is_ignored(capsys, post, since):
    fake = FakeGitHub(labels=[post], issues=[tracked_issue(98897)])
    assert run(fake, "--since", since) == 0
    assert fake.issue_writes() == []
    out = actions(capsys.readouterr().out)
    assert out[0].startswith("IGNORE-COSMETIC changelog:98897 ")
    assert out[-1] == "SUMMARY open=0 comment=0 digest=0 skip-dup=0 ignore-cosmetic=1"


# Tier-1 posts in the labels fixture with since 2026-08-01, as computed by the
# classifier: every deprecations-type post, posts naming tier-1 terms, posts
# with an editor's note that names a date, and the edit of 90922 (note 2026-08-25).
# 98447 and 99443 have notes that say only "Updated ..." and stay out.
BACKFILL_TIER1 = {
    90922, 96959, 98036, 98515, 98669, 98796, 98897, 99010,
    99144, 99185, 99215, 99219, 99261, 99283, 99939,
}  # fmt: skip


def test_backfill_classification():
    since = W.parse_time("2026-08-01")
    tier1 = {p["id"] for p in LABELS if W.classify(W.item_from_post(p), since).tier == 1}
    assert {98897, 99939} <= tier1
    assert len(tier1) == 15
    assert tier1 == BACKFILL_TIER1


def test_backfill_dry_run_and_rerun(capsys):
    fake = FakeGitHub(labels=LABELS, deprecations=DEPRECATIONS)
    assert run(fake, "--since", "2026-08-01T00:00:00Z", "--dry-run") == 0
    assert fake.writes == []
    out = actions(capsys.readouterr().out)
    opens = [ln for ln in out if ln.startswith("DRY-RUN OPEN ")]
    assert len(opens) == 5  # default --max-issues
    assert any(ln.startswith("DRY-RUN OPEN changelog:98897 ") for ln in opens)
    assert any(ln.startswith("DRY-RUN OPEN changelog:99939 ") for ln in opens)
    # 17 label tier-1 posts plus 99517 from the deprecations fixture, minus 5 opened.
    assert sum(OVER_CAP_TEXT in ln for ln in out) == 11
    assert out[-1].startswith("DRY-RUN SUMMARY open=5 comment=0 ")
    assert sum(ln.startswith("DRY-RUN IGNORE-COSMETIC ") for ln in out) == 8

    assert run(fake, "--since", "2026-08-01T00:00:00Z", "--max-issues", "100") == 0
    first = len(fake.issue_writes())
    created = {
        int(group1(r":id=(\d+) -->", w[2]["body"]))
        for w in fake.opened()
        if "platform-change" in w[2]["labels"]
    }
    assert created == BACKFILL_TIER1 | {99517}
    capsys.readouterr()

    assert run(fake, "--since", "2026-08-01T00:00:00Z", "--max-issues", "100") == 0
    assert len(fake.issue_writes()) == first
    out = actions(capsys.readouterr().out)
    assert out[-1].startswith("SUMMARY open=0 comment=0 digest=0 ")


OVER_CAP_TEXT = "(over the per-run cap)"


def test_deprecations_are_tier1_by_type():
    since = W.parse_time("2026-10-15")  # both posts are new
    for post in DEPRECATIONS:
        verdict = W.classify(W.item_from_post(post), since)
        assert verdict.tier == 1
        assert verdict.reasons[0] == "deprecations type"


def test_deprecation_type_without_terms_is_tier1():
    post = {**DEPRECATIONS[0], "title": {"rendered": "Plain"}, "content": {"rendered": "<p>x</p>"}}
    verdict = W.classify(W.item_from_post(post), W.parse_time("2026-10-25"))
    assert (verdict.tier, verdict.reasons) == (1, ["deprecations type"])


TIER2_IDS = (99823, 99870, 99751)  # runner, enforce, runner


def test_digest_renders_and_closes_older_week(capsys):
    posts = [p for p in LABELS if p["id"] in TIER2_IDS]
    old = {
        "number": 3,
        "title": "[platform-change] Weekly digest 2026-W45",
        "body": "old\n\n<!-- platform-change-digest:2026-W45 -->\n",
        "state": "open",
        "labels": [{"name": "platform-change-digest"}],
    }
    fake = FakeGitHub(labels=posts, issues=[old])
    assert run(fake, "--since", "2026-10-27") == 0

    created = fake.opened()
    assert len(created) == 1
    data = created[0][2]
    assert data["title"] == "[platform-change] Weekly digest 2026-W46"
    assert data["labels"] == ["platform-change-digest"]
    lines = [ln for ln in data["body"].splitlines() if ln.startswith("- ")]
    assert len(lines) == 3
    assert lines == sorted(lines)
    assert any(
        ln.startswith("- 2026-10-28 [GitHub Actions workflow lockfiles are generally available](")
        and ln.endswith(") — enforce")
        for ln in lines
    )
    assert "<!-- platform-change-digest:2026-W46 -->" in data["body"]

    new_number = fake.issues[-1]["number"]
    assert fake.comments[3] == [{"body": f"Superseded by #{new_number}."}]
    assert old_state(fake, 3) == "closed"
    out = actions(capsys.readouterr().out)
    assert sum(ln.startswith("DIGEST changelog:") for ln in out) == 3
    assert f"DIGEST 2026-W45 #3 closed (superseded by #{new_number})" in out

    # Same week, one more item: the body is rewritten in place, deduped by link.
    more = posts + [p for p in LABELS if p["id"] == 99618]
    fake.labels = more
    assert run(fake, "--since", "2026-10-20") == 0
    assert len(fake.opened()) == 1
    patch = fake.writes[-1]
    assert patch[:2] == ("PATCH", f"issues/{new_number}")
    lines = [ln for ln in patch[2]["body"].splitlines() if ln.startswith("- ")]
    assert len(lines) == 4
    assert len({group1(r"\]\((\S+)\)", ln) for ln in lines}) == 4


def old_state(fake, number):
    return next(i["state"] for i in fake.issues if i["number"] == number)


def test_max_issues_overflows_into_digest(capsys):
    fake = FakeGitHub(labels=LABELS)
    assert run(fake, "--since", "2026-08-01", "--max-issues", "1") == 0
    tier1_issues = [w for w in fake.opened() if w[2]["labels"] == ["platform-change"]]
    assert len(tier1_issues) == 1
    assert "id=99939 -->" in tier1_issues[0][2]["body"]  # most matched signals
    digest = [w for w in fake.opened() if w[2]["labels"] == ["platform-change-digest"]]
    assert len(digest) == 1
    over = [ln for ln in digest[0][2]["body"].splitlines() if ln.endswith(OVER_CAP_TEXT)]
    assert len(over) == len(BACKFILL_TIER1) - 1
    assert any("generally-available" in ln and "98897" not in ln for ln in over)
    assert any("workflow-execution-protections" in ln for ln in over)


def test_rest_html_falls_back_to_rss(capsys):
    rss = {W.LABEL_FEEDS[0]: RSS_ACTIONS}
    fake = FakeGitHub(rest_body="<!DOCTYPE html><html><body>maintenance</body></html>", rss=rss)
    assert run(fake, "--since", "2026-10-25", "--dry-run") == 0
    out = capsys.readouterr().out
    assert "::warning::changelog REST API returned a non-JSON body" in out
    assert "::warning::reading RSS feeds instead" in out
    assert any(ln.startswith("DRY-RUN OPEN changelog:99939 ") for ln in out.splitlines())
    fetched = [
        r.target for r in fake.requests if r.target.startswith("https://github.blog/changelog/")
    ]
    assert fetched == [*W.LABEL_FEEDS, W.DEPRECATIONS_FEED]


def test_rss_items_have_no_edit_detection():
    items = {i.id: i for i in W.items_from_rss(RSS_ACTIONS, deprecations=False)}
    assert items[99939].types == [W.DEPRECATIONS_TYPE]  # changelog-type Retired
    assert items[99939].published == datetime(2026, 11, 2, 15, 14, 46, tzinfo=UTC)
    assert all(i.modified == i.published for i in items.values())


def test_runner_images_classified_by_terms(capsys):
    since = W.parse_time("2026-09-01")
    verdicts = {i["number"]: W.classify(W.item_from_runner_issue(i), since) for i in RUNNER_IMAGES}
    assert {n: v.tier for n, v in verdicts.items()} == {
        15253: 2,
        15226: 1,
        14856: 2,
        14826: 2,
        14818: 2,
    }
    assert verdicts[15226].reasons == ["brownout"]
    assert "runner" in verdicts[15253].reasons

    fake = FakeGitHub(runner_images=RUNNER_IMAGES)
    assert run(fake, "--since", "2026-09-01") == 0
    opened = fake.opened()
    assert "<!-- platform-change:source=runner-images:id=15226 -->" in opened[0][2]["body"]
    assert len(opened) == 2  # the brownout issue and the digest


def test_runner_images_by_creation_date():
    old = {**RUNNER_IMAGES[0], "created_at": "2026-08-01T00:00:00Z"}
    verdict = W.classify(W.item_from_runner_issue(old), W.parse_time("2026-11-05"))
    assert verdict.kind == "cosmetic"


def test_wp_pagination_reads_every_page():
    fake = FakeGitHub(labels=LABELS, wp_pages=3)
    assert run(fake, "--since", "2026-08-01", "--dry-run") == 0
    pages = [
        parse_qs(urlsplit(r.target).query)["page"][0]
        for r in fake.requests
        if r.target.startswith(W.CHANGELOG_API) and "label=" in r.target
    ]
    assert pages == ["1", "2", "3"]


def test_query_widens_modified_after_and_requests_gmt():
    fake = FakeGitHub()
    assert run(fake, "--since", "2026-10-20T06:00:00Z", "--dry-run") == 0
    q = parse_qs(urlsplit(fake.requests[0].target).query)
    assert q["modified_after"] == ["2026-10-19T06:00:00"]
    assert q["label"] == ["1836,3628,3630"]
    assert "date_gmt" in q["_fields"][0].split(",")


def test_gmt_fields_take_precedence():
    post = {
        **PUBLISHED[0],
        "date_gmt": "2026-09-17T15:45:26",
        "modified_gmt": "2026-09-17T15:45:26",
    }
    item = W.item_from_post(post)
    assert item.published == datetime(2026, 9, 17, 15, 45, 26, tzinfo=UTC)


def test_every_source_failing_exits_nonzero(capsys):
    fake = FakeGitHub(rest_body="not json", fail_runner_images=True)
    assert run(fake, "--since", "2026-10-25", "--dry-run") == 1
    assert "::error::every source failed" in capsys.readouterr().out


def test_one_source_failing_still_runs(capsys):
    fake = FakeGitHub(deprecations=DEPRECATIONS, fail_runner_images=True)
    assert run(fake, "--since", "2026-10-25", "--dry-run") == 0
    out = capsys.readouterr().out
    assert "::warning::source failed: runner-images announcements" in out
    assert "DRY-RUN OPEN changelog:99939 " in out


def test_since_defaults_to_74_hours(capsys):
    fake = FakeGitHub()
    assert run(fake, "--dry-run") == 0
    runner = next(r for r in fake.requests if r.target.startswith(W.RUNNER_IMAGES_ISSUES))
    assert "since=2026-11-06T10:00:00Z" in runner.target


def test_markers_in_external_text_are_escaped():
    post = copy.deepcopy(EDITED[0])
    post["content"]["rendered"] += (
        "<p>pull_request_target &lt;!-- platform-change:source=changelog:id=1 --&gt; @octocat</p>"
    )
    item = W.item_from_post(post)
    body = W.issue_body(item, W.classify(item, W.parse_time("2026-10-20")))
    assert "id=1 -->" not in body
    assert "@octocat" not in body


def test_parse_json_pages_handles_concatenated_arrays():
    assert W.parse_json_pages("[1,2]\n[3]") == [1, 2, 3]
    assert W.parse_json_pages("") == []


def test_editor_note_parsing():
    text = W.html_to_text(EDITED[0]["content"]["rendered"])
    notes = W.editor_notes(text)
    assert len(notes) == 1
    assert notes[0].when == datetime(2026, 10, 21).date()
    assert notes[0].text.startswith("We updated the automatic enforcement date")
    assert W.parse_note_date("October 14th, 2026") == datetime(2026, 10, 14).date()


def test_note_dated_previous_pacific_day_still_counts(capsys):
    # Saved 2026-10-22 06:40 UTC, still October 21 in Pacific time; the next run starts after it.
    post = {
        **EDITED[0],
        "modified": "2026-10-21T23:40:00",
        "modified_gmt": "2026-10-22T06:40:00",
        "date_gmt": "2026-09-17T08:45:26",
    }
    fake = FakeGitHub(labels=[post], issues=[tracked_issue(98897)])
    since = "2026-10-22T04:23:00Z"
    assert run(fake, "--since", since, now=datetime(2026, 10, 23, 6, 23, tzinfo=UTC)) == 0
    line = actions(capsys.readouterr().out)[0]
    assert line.startswith("COMMENT changelog:98897 ")
    assert line.endswith(" #7 modified 2026-10-22T06:40:00Z")


def test_unlabelled_workflow_issue_still_dedupes(capsys):
    issue = {**tracked_issue(98897), "labels": [], "user": {"login": "github-actions[bot]"}}
    fake = FakeGitHub(labels=PUBLISHED, issues=[issue])
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    assert fake.opened() == []
    line = actions(capsys.readouterr().out)[0]
    assert line.startswith("SKIP-DUP changelog:98897 ") and line.endswith(" #7")


def test_note_added_while_post_is_new_gets_a_comment(capsys):
    fake = FakeGitHub(labels=PUBLISHED)
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    fake.labels = [{**EDITED[0], "modified": "2026-09-18T01:00:00"}]
    note = {**EDITED[0]["content"]}
    note["rendered"] = note["rendered"].replace("October 21, 2026", "September 18, 2026")
    fake.labels[0]["content"] = note
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    out = actions(capsys.readouterr().out)
    assert any(ln.startswith("COMMENT changelog:98897 ") for ln in out)
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    out = actions(capsys.readouterr().out)
    assert any(ln.startswith("SKIP-DUP changelog:98897 ") for ln in out)


def test_unparseable_note_date_on_edit_is_tier1():
    post = copy.deepcopy(EDITED[0])
    post["content"]["rendered"] = post["content"]["rendered"].replace(
        "October 21, 2026)", "21st of October)"
    )
    verdict = W.classify(W.item_from_post(post), W.parse_time("2026-10-20"))
    assert verdict.kind == "edit" and verdict.tier == 1


def test_note_without_a_date_does_not_make_a_new_post_tier1():
    note = W.Note("August 27, 2026", None, "Updated the Community link.")
    assert not W.mentions_date_change(note)


def test_resave_after_comment_does_not_repeat_the_note(capsys):
    fake = FakeGitHub(labels=EDITED, issues=[tracked_issue(98897)])
    assert run(fake, "--since", "2026-10-20") == 0
    fake.labels = [{**EDITED[0], "modified": "2026-10-23T08:00:00"}]
    assert run(fake, "--since", "2026-10-21", now=datetime(2026, 10, 24, 10, 0, tzinfo=UTC)) == 0
    assert len(fake.comments[7]) == 1
    assert actions(capsys.readouterr().out)[-2].startswith("SKIP-DUP changelog:98897 ")


def test_resave_of_new_post_with_note_stays_quiet(capsys):
    fake = FakeGitHub(labels=EDITED)
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    fake.labels = [{**EDITED[0], "modified": "2026-10-22T18:00:00"}]
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    assert fake.comments == {}


@pytest.mark.parametrize(
    "link",
    [
        "https://github.blog/x)\n<!-- platform-change:source=changelog:id=1 -->",
        "http://github.blog/changelog/x",
        "https://example.com/changelog/x",
    ],
)
def test_unexpected_links_are_skipped(capsys, link):
    fake = FakeGitHub(labels=[{**PUBLISHED[0], "link": link}])
    assert run(fake, "--since", "2026-09-16T00:00:00Z") == 0
    assert fake.opened() == []
    assert "unexpected link" in capsys.readouterr().out
