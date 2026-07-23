# Shell Scripting

## `gh api --paginate` and jq

`gh api --paginate` applies the `--jq` expression **independently to each page** of results, not to the combined output. This is a documented `gh` CLI behavior and a common source of bugs.

**Do not** use aggregating jq filters directly in `--jq` with `--paginate`:

```bash
# WRONG — `length` runs per-page; produces one number per page, not a total
count=$(gh api --paginate /repos/{owner}/{repo}/issues/comments --jq 'length')
```

**Do** collect all pages first, then pipe to a separate `jq -s` (slurp) call. `jq -s` slurps the input into an array; use `add` to flatten before aggregating:

```bash
# CORRECT — slurp all pages, flatten with add, then aggregate
count=$(gh api --paginate /repos/{owner}/{repo}/issues/comments | jq -s 'add | length')
```

Without `--jq`, `gh api --paginate` merges all page arrays into a single flat JSON array before writing to stdout. `jq -s` then wraps that into an array-of-one; `add` unwraps it back to the flat array, and the aggregating filter runs once over all items. This pattern is defensive — it works correctly whether the upstream emits one merged array or (as when `--jq` is present) one array per page.

This applies to any aggregating filter: `length`, `sort_by`, `group_by`, `add`, `min_by`, `max_by`, etc. If the filter only selects or transforms individual items (e.g., `.[] | .id`), per-page application is fine — but pipe the result through a final `jq -s` step before any cross-page aggregation.

**When reviewing shell scripts:** Flag `--paginate --jq '... | length'` (or any other aggregating filter in `--jq`) as a medium-severity finding. The fix is always to move the aggregation to a separate `| jq -s 'add | ...'` pipe.

**Alternative — `--slurp` flag:** When no inline `--jq` transform is needed, `gh api --paginate --slurp` combines pages into a single array directly. However, `--slurp` is mutually exclusive with `--jq` (errors with `"the --slurp option is not supported with --jq or --template"`), so the `| jq -s 'add | ...'` pipe pattern is required whenever you also need per-item filtering.
