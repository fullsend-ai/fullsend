# Post-Flight Verification

Part of the [cutting-releases](SKILL.md) skill.

Run after the version tag is pushed and the CI workflows complete.
The release workflow automatically moves the `v0` floating tag after
GoReleaser succeeds (skipped for pre-release tags). Focus on the areas identified during pre-flight
step F.

## A. Wait for CI workflows

Wait for the Release workflow and the Sandbox Images workflow, both
triggered by the `v*` tag push, to complete. Sandbox Images also runs
on `main` pushes, so scope both lists to the tag:

```
gh run list --workflow=release.yml --branch <tag> --limit=1
gh run list --workflow=sandbox-images.yml --branch <tag> --limit=1
```

Both must pass before proceeding. If either fails, investigate and
resolve before continuing — a broken release or sandbox image affects
all downstream consumers.

## B. Verify the release artifacts

```
gh release view <tag>
```

Check that the title, changelog, and binary assets look correct.
Verify the release is not marked as a draft.

## B2. Verify agents validation and tag

The release workflow gates **publication itself** on functional test
validation. `resolve-agents` verifies the tag and checks the gate
secrets, `validate-agents` runs agents' functional tests against the
release tag, and only then does `release` run GoReleaser. `tag-agents`
pushes the version tag to agents last. Every failure path sends a Slack
notification.

Verify the release jobs succeeded:

```
gh run view <run-id> --repo fullsend-ai/fullsend --json jobs \
  --jq '.jobs[] | select(.name | test("^(resolve-agents|validate-agents|recheck-tag|release|tag-agents)")) | {name, conclusion}'
```

`validate-agents / gate` is skipped on tag pushes; that is expected.
If `resolve-agents`, `validate-agents` or `recheck-tag` failed (the
last means the tag moved during the gate), the release was
blocked before publishing: no binaries, no GitHub Release, no moved
`v0` tag, and no agents tag. Step B above will show nothing to verify.
Pick the recovery from "When a release run fails" in the SKILL.md
Notes: a flake, an agents-only fix and a fullsend fix each need a
different action.

If only `tag-agents` failed, the fullsend release shipped but agents
was not tagged; fix that job's cause and re-run it.

If all jobs succeeded, verify the tag and release exist on agents:

```
gh release view <tag> --repo fullsend-ai/agents
```

Verify the agents tag includes the repin (SKILL.md step 8). The tag
is the agents `main` commit the gate validated, which can be later than
the repin merge:

```
gh api repos/fullsend-ai/agents/compare/<repin-merge-sha>...<tag> --jq .status
```

`<repin-merge-sha>` was recorded in SKILL.md step 8.4.

`identical` or `ahead` passes; anything else means the release shipped
without the repinned images.

For non-prerelease tags, verify `v0` moved on both repos. Each pair
must print the same SHA (`commits/` resolves the annotated fullsend
tag to its commit; agents tags are lightweight):

```
gh api repos/fullsend-ai/fullsend/commits/<tag> --jq .sha
gh api repos/fullsend-ai/fullsend/git/ref/tags/v0 --jq '.object.sha'
gh api repos/fullsend-ai/agents/git/ref/tags/<tag> --jq '.object.sha'
gh api repos/fullsend-ai/agents/git/ref/tags/v0 --jq '.object.sha'
```

If the agents release workflow failed, investigate before continuing —
downstream consumers may reference agents by tag.

## B3. Verify the harness image pins

The released agents harness files must pin the RC's digests from
SKILL.md step 8, not a fresh `skopeo inspect` of `X.Y.Z` — the final's
images are a separate, non-reproducible build with different digests:

```
for f in $(gh api "repos/fullsend-ai/agents/contents/harness?ref=<tag>" --jq '.[].name'); do gh api "repos/fullsend-ai/agents/contents/harness/$f?ref=<tag>" --jq ".content|@base64d|split(\"\n\")[]|select(test(\"image:.*fullsend-(sandbox|code)\"))|\"$f: \"+."; done
```

Expect one line per harness that pins an image, each with an RC digest.
Empty output or an error means the check did not run — treat it as a
failure, not a pass.

## C. Skip fullsend-ai repos

The `fullsend-ai/.fullsend` repo references reusable workflows via
`@main`, not `@v0`. Its runs do **not** exercise the `v0` tag and
cannot confirm that the tag move worked. (Those runs are checked
during pre-flight instead, as a signal that `main` is healthy.)

Skip fullsend-ai for post-flight `v0` verification. Focus on other
downstream consumers in step D.

## D. Check additional downstream repos (optional)

Use `AskUserQuestion` to ask if the user has access to additional
downstream orgs:

> Do you have access to any other downstream orgs/repos to verify?
> (e.g. "konflux-ci, redhat-developer/rhdh-agentic")
> Leave blank to skip.

For each repo provided, check recent workflow runs that started
**after** the `v0` tag move:

```
gh run list --repo <org/repo> --limit=5
```

Confirm they completed without workflow-resolution errors (e.g.
"could not find reusable workflow"). If no runs occurred naturally,
check for recent failed runs that can be retriggered:

```
gh run list --repo <org/repo> --status=failure --limit=3
```

Present any candidate to the user for confirmation before retriggering:

> I found run `<run-id>` (failed) in `<org/repo>`.
> Retrigger it to verify `@v0` resolves?

Once confirmed:

```
gh run rerun <run-id> --failed --repo <org/repo>
```

If blank, skip this step — not all admins have access to every
enrolled org.

## E. Present post-flight summary

Summarize results to the user:

| Org/Repo | `@v0` Refs | Status |
|----------|-----------|--------|
| ... | ... | ... |

Note: `fullsend-ai` repos are excluded from this table — they use
`@main` and were checked during pre-flight.

Also report B2 (repin included, `v0` moved on both repos) and B3
(harness pins match the RC digests). A failure in either is a blocker.

Distinguish between:
- **Release-related failures** — workflow resolution errors, missing
  secrets, or permission failures caused by the tag move.
- **Unrelated failures** — agent runtime errors, external API issues,
  or pre-existing test failures.
