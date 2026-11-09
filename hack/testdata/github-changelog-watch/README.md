# github-changelog-watch fixtures

Recorded 2026-11-09 from the public sources the watcher polls.

| File | Source |
|---|---|
| `changelogs-labels-2026-08-01-to-2026-11-09.json` | `https://github.blog/wp-json/wp/v2/changelogs?label=1836,3628,3630&modified_after=2026-08-01T00:00:00&orderby=modified&per_page=100&_fields=id,date,modified,link,title,content,type,label` (70 posts, `X-WP-Total: 70`) |
| `changelog-98897-edited-2026-10-21.json` | Post 98897 from the file above: published 2026-09-17, `modified` 2026-10-21T09:18, with the editor's note that moved enforcement to December 2 |
| `changelog-98897-published-2026-09-17.json` | Post 98897 as published: built from the edited copy by removing the editor's note, restoring the original sentence "On November 2, 2026, we&rsquo;ll automatically enforce" (Wayback copy `20260917190353`), and setting `modified` to `date` |
| `changelogs-deprecations-2026-10-25-to-2026-11-09.json` | `.../changelogs?type=3523&modified_after=2026-10-25T00:00:00&orderby=modified&per_page=100&_fields=...` |
| `rss-label-actions-2026-11-09.xml` | `https://github.blog/changelog/label/actions/feed/` (RSS fallback) |
| `runner-images-announcements-2026-11-09.json` | `gh api "repos/actions/runner-images/issues?labels=Announcement&state=all&per_page=5&sort=created"`, trimmed to number, title, html_url, state, created_at, updated_at, label names, body |

Post bodies are HTML with entities (`Editor&rsquo;s note`), so match on unescaped, tag-stripped text.
