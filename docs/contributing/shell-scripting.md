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

## yq/jq pitfalls

yq and jq share similar syntax but have different built-in function sets. Using a jq-only function in a yq expression causes a parse error (non-zero exit, stderr message). It becomes a silent failure only when combined with error suppression (see "Fail-open error suppression" below).

Common mistakes:

- `downcase` is the correct yq function for lowercasing strings. `ascii_downcase` (from jq) does not exist in yq and causes a parse error.
- `ascii_upcase` (jq) vs `upcase` (yq) — same pattern, different function names.
- `ltrimstr`/`rtrimstr` (jq) have no yq equivalent — yq only has `trim` (whitespace-only). `split("str")` (single argument, literal string) works the same in both. jq's 2-arg `split(regex; flags)` form is the one case in this list that does **not** produce a parse error: yq accepts a second argument to `split` without erroring (exit 0, no stderr), but its behavior does not match jq's — verified output diverges unpredictably depending on the pattern and second argument, rather than failing loudly. Don't assume 2-arg `split` behaves like jq; test the actual output for your specific case, or restrict yourself to the single-argument literal form.

When generating yq expressions in workflow YAML, verify the function exists in yq's built-in set, not jq's. The [yq built-in operators documentation](https://mikefarah.gitbook.io/yq/operators) is the reference.

**When reviewing PRs:** Flag unrecognized yq function names (e.g., jq-only functions used in yq expressions) as a medium-severity finding. The fix is to replace the function with its yq equivalent.

## Fail-open error suppression

`2>/dev/null || echo ""` (or `|| true`, `|| :`) suppresses errors and provides a fallback value. Whether this pattern is acceptable depends on what the step does:

- **Non-critical steps** (logging, metrics, optional annotations): fail-open is acceptable. A silent failure in a logging step does not affect correctness.
- **Gate/guard steps** (dispatch routing, feature flags, conditional execution, skip/include logic): fail-open is **dangerous**. Silent failure in a gate means the gate never fires — the guarded behavior either always executes or never executes, depending on how the fallback value is interpreted downstream.

```bash
# DANGEROUS — if yq fails (e.g., invalid function), the gate silently
# passes through with an empty string, making the conditional meaningless
label=$(echo "$yaml" | yq '.metadata.labels.env // ""' 2>/dev/null || echo "")

# SAFER — capture stderr, emit a warning on failure, fail closed
if ! label=$(echo "$yaml" | yq '.metadata.labels.env // ""' 2>&1); then
  echo "::warning::yq failed: $label"
  exit 1
fi
```

**When writing workflow steps:** If a step gates dispatch, skips execution, or controls conditional logic, ensure its error path surfaces the failure (e.g., via `::warning::`) rather than swallowing it silently — capture stderr to a variable and emit a warning on failure, instead of `2>/dev/null`. Whether to then fail closed (`exit 1`) or deliberately proceed depends on the gate's purpose: authorization/security gates must fail closed (see `scripts/check-e2e-authorization.sh`, which traps errors and sets `authorized=false`), while feature/agent-enablement gates may deliberately warn-and-proceed (availability over strictness — an operator would rather a stage run than block on a transient parse error). The DANGEROUS/SAFER examples above illustrate the difference between silent and surfaced failure, not a universal mandate for `exit 1`.

**When reviewing PRs:** Flag `2>/dev/null || echo ""` (or `|| true`) in workflow steps that gate dispatch or skip execution as a medium-severity or higher finding — unless the fallback value is verified to be restrictive (narrows what's allowed) rather than permissive (widens what's allowed) downstream. Silent failure in a permissive-fallback gate means the gate is non-functional; the same pattern feeding a restrictive fallback is comparatively safe (it fails toward "allow nothing" rather than "allow everything"). Judge severity by what the fallback value actually does downstream, not by the presence of `2>/dev/null || echo ""`/`|| true` alone.

## `| grep -q` under `pipefail`

`grep -q` exits as soon as it finds a match and closes the pipe. If the writer still has output to send, its next write hits the closed pipe and it is killed by SIGPIPE (exit 141). Under `set -o pipefail` the pipeline then reports 141 instead of `grep`'s 0, so a check that matched still fails. Whether it fails depends on scheduling: writers flush to a pipe in blocks (often 4 KB), so the race only appears once the output needs more than one write, and it can pass on CI while failing everywhere else.

```bash
# RACY — fails intermittently once the help text exceeds one pipe write
if bash ./script.sh --help | grep -q '^Usage:'; then ...

# SAFE — capture the output first, then match it
help_out=$(bash ./script.sh --help)
if grep -q '^Usage:' <<< "${help_out}"; then ...
```

Capturing first also keeps the check strict: under `set -e`, a non-zero exit from the writer aborts the script at the assignment. `grep -m1` has the same race as `grep -q`.

**When reviewing PRs:** Flag `<writer> | grep -q` under `pipefail` when the writer can produce more than about 4 KB of output (help text, logs, API responses) as a low-to-medium-severity finding.

## Stdout contamination in command substitution

When a shell function is called inside `$(...)` command substitution, **everything the function writes to stdout becomes part of the captured variable**. If the function also writes diagnostic output (log messages, retry warnings, `::error::`/`::warning::` annotations) to stdout, that output is concatenated with the intended return value, silently corrupting it.

This is a common source of bugs in shell scripts that use helper functions with retry logic or error reporting. The captured variable may contain a mix of JSON and diagnostic text, causing downstream `jq`/`yq` parsing to fail silently or produce wrong results.

```bash
# WRONG — echo writes to stdout, contaminating the captured variable
retry_curl() {
  local attempt=1 max=3 delay=5
  while true; do
    if curl "$@"; then return 0; fi
    if (( attempt >= max )); then
      echo "::error::Request failed after ${max} attempts"
      return 1
    fi
    echo "::warning::Attempt ${attempt}/${max} failed, retrying in ${delay}s..."
    sleep "${delay}"
    (( attempt++ ))
  done
}

# resp now contains "::warning::Attempt 1/3 failed, retrying in 5s...\n{json}"
# jq parsing will fail or produce wrong results
resp=$(retry_curl -fsSL "https://api.example.com/data")
tag=$(echo "${resp}" | jq -r '.tag_name')
```

**Direct all non-value output to stderr** using `>&2`:

```bash
# CORRECT — diagnostic output goes to stderr, only curl's stdout is captured
retry_curl() {
  local attempt=1 max=3 delay=5
  while true; do
    if curl "$@"; then return 0; fi
    if (( attempt >= max )); then
      echo "::error::Request failed after ${max} attempts" >&2
      return 1
    fi
    echo "::warning::Attempt ${attempt}/${max} failed, retrying in ${delay}s..." >&2
    sleep "${delay}"
    (( attempt++ ))
  done
}

# resp contains only the curl response — jq parsing works correctly
resp=$(retry_curl -fsSL "https://api.example.com/data")
tag=$(echo "${resp}" | jq -r '.tag_name')
```

The same rule applies to any function output that is not the intended return value: progress messages, debug logging, `echo "Retrying..."`, and GitHub Actions annotations (`::warning::`, `::error::`, `::notice::`). If the function might be called inside `$(...)`, all of these must use `>&2`.

**When reviewing PRs:** If a shell function is used inside `$(...)` or backtick command substitution, verify that all non-value output (log messages, warnings, `::error::`/`::warning::` annotations, retry diagnostics) is directed to stderr. Flag stdout contamination as medium-severity when the captured value is used in conditional logic or parsed by `jq`/`yq`, and as high-severity when it feeds into security-sensitive operations (token handling, authorization checks, cryptographic verification).

## Bot and app collaborator permissions (`none` permission)

The collaborator permission endpoint
(`/repos/{owner}/{repo}/collaborators/{username}/permission`) returns HTTP 200
with `{"permission": "none", "role_name": ""}` for GitHub Apps and bots (e.g.,
`renovate[bot]`, `dependabot[bot]`). Scripts parsing `role_name` or `permission`
must accept `none` as an explicit non-write role rather than treating an empty
`role_name` or `permission: "none"` as an unexpected API error.

```bash
# Handle bot/app permission response with empty role_name
case "${perm}" in
  admin|write|read|none) echo "${perm}" ;;
  *) return 1 ;; # unexpected response format
esac
```

**When reviewing PRs:** Flag collaborator permission parsing that treats
`permission: "none"` or an empty `role_name` as a hard API error as a
low-to-medium-severity finding.

## Concurrent label deletion (HTTP 404 race)

When multiple workflows trigger concurrently on the same PR event (e.g.,
separate behaviour and functional test runs) and both attempt
`gh api -X DELETE repos/{owner}/{repo}/issues/{pr}/labels/{label}`, the second
caller receives `HTTP 404 (Label does not exist)`. Gate cleanup functions must
treat HTTP 404 on DELETE as already removed (`return 0`) rather than failing
closed with an API error.

```bash
# SAFE — treat HTTP 404 on label DELETE as already removed
del_err="$(mktemp)"
if ! gh api -X DELETE "repos/${REPO}/issues/${PR}/labels/${LABEL}" >/dev/null 2>"${del_err}"; then
  if grep -qiE "HTTP 404|Label does not exist" "${del_err}"; then
    rm -f "${del_err}"
    return 0
  fi
  cat "${del_err}" >&2
  rm -f "${del_err}"
  return 1
fi
rm -f "${del_err}"
```

**When reviewing PRs:** Flag label deletion steps in gate or cleanup scripts
that do not tolerate HTTP 404 as a medium-severity finding.
