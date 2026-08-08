# Pre-script output v1

The **pre-script output protocol** — the contract between `fullsend run` and a
harness `pre_script` ([issue #4718](https://github.com/fullsend-ai/fullsend/issues/4718)).

A pre-script uses this protocol to tell `fullsend run` **not to run the agent** —
for example when an open PR already addresses the issue. The decision lives in
the CLI rather than in workflow YAML, so every forge inherits it: GitLab and
Forgejo scaffolds invoke `fullsend run` directly and get the same gating with no
CI-side reimplementation.

This document is normative. The Go implementation lives in
[`internal/prescript`](../../../../internal/prescript/prescript.go).

## Protocol

`fullsend run` creates an empty file inside the run directory and exports its
absolute path to the pre-script:

```
FULLSEND_PRESCRIPT_OUTPUT=/path/to/run-dir/prescript-1234567890.out
```

The script **appends** `key=value` lines to that file. After the script exits
successfully, `fullsend run` parses the file:

| `skipped` | Behavior |
|-----------|----------|
| `true` | Report a `skipped` status (⏭️), relay outputs, exit 0 **before sandbox creation**. |
| `false`, absent, or empty file | Proceed with the run — today's behavior. |

### Exit code 78 — neutral skip

Exit code 78 is an alternative way to request a skip ([issue #582](https://github.com/fullsend-ai/fullsend/issues/582)).
When `fullsend run` sees exit code 78 it treats the run as
skipped/neutral — identical to `skipped=true` in the output file. The
code follows the CI convention for "neutral" (used by GitHub Actions and
others).

| Exit code | Behavior |
|-----------|----------|
| 0 | Parse the output file; `skipped=true` requests a skip. |
| 78 | Skip unconditionally. Output file is parsed best-effort for `reason` and other outputs; a parse error does not block the skip. |
| Any other non-zero | Hard failure, unchanged by this protocol. |

Exit 78 is complementary to the file-based `skipped=true` mechanism.
Either one alone is sufficient to request a skip. When a script exits 78,
the skip proceeds even if the output file says `skipped=false` or is
malformed — the exit code is authoritative.

**Stdout as fallback reason:** When a script exits 78 and no `reason` key
is found in the output file, `fullsend run` uses the last non-empty line
of the script's stdout as the skip reason. This lets simple scripts
communicate a reason without using the output file at all:

```sh
echo "No issues need scoring"
exit 78
```

### Reserved keys

| Key | Required | Meaning |
|-----|----------|---------|
| `skipped` | no | `true` or `false`. Any other value — including an empty one — is an error. |
| `reason` | no | Short human-readable explanation, shown in the run log and in the status comment. |

Reserved keys are **lowercase**. A key differing only in case (`SKIPPED=true`)
is a hard error rather than an unrelated output — see [Error semantics](#error-semantics).

### Other keys

Any other valid key is parsed, logged, and relayed. Reserved-key names are
owned by the protocol; a future CLI may interpret additional lowercase
single-word keys, so scripts should prefix their own outputs (`myagent_pr=123`)
to avoid colliding with a future directive.

## Grammar

Line-based. One assignment per line:

```
skipped=true
reason=an open PR already addresses this issue
# comments and blank lines are ignored
myagent_existing_pr=123
```

- **Keys** match `^[a-zA-Z_][a-zA-Z0-9_-]*$` (hyphens allowed).
- **Values** are single-line and may not contain control characters
  (`U+0000`–`U+001F`, `U+007F`). This includes `\r` and `\n`.
- **Whitespace** surrounding the key and the value is insignificant:
  `skipped = true` is equivalent to `skipped=true`.
- **Comments** are lines whose first non-whitespace character is `#`. A value
  may contain `#`; only a leading `#` starts a comment.
- **`=` in values** is preserved — only the first `=` splits the line.
- **Repeated keys**: the last assignment wins.
- **Maximum file size** is 1 MiB.

### Not supported

The format resembles `GITHUB_OUTPUT` but is **not compatible with it**:

- No `key<<DELIMITER` heredoc form. Multiline values are not expressible;
  a heredoc line is a hard error with a message naming this limitation.
- No quoting. `skipped="true"` is an error, not `true`.

## Error semantics

These are **hard errors** — the run fails rather than proceeding:

| Condition | Rationale |
|-----------|-----------|
| A line that is not `key=value` | A typo like `skipped true` silently ignored would start the duplicate agent run the pre-check exists to prevent. |
| A key that fails the key pattern | Same. |
| A key differing from a reserved key only by case | `SKIPPED=true` would otherwise be stored as an unrelated output and the run would proceed silently. |
| A value containing a control character | A bare `\r` is a line terminator to the GitHub Actions runner, so such a value could smuggle extra output entries — including overriding `skipped`. |
| `skipped` set to anything but `true`/`false` | Same silent-proceed risk. |
| `skipped` present but empty (`skipped=`) | `skipped=${SKIP}` with `SKIP` unset is a plausible script bug, and an intended skip must not degrade into a duplicate run. An *absent* `skipped` key means proceed; a present-but-empty one does not. |
| The output file missing after the script ran | `fullsend run` created it beforehand, so its absence means the script removed it. |
| File larger than 1 MiB | Bounded read. |

Failing closed is deliberate: an unparseable skip request must not degrade into
a duplicate agent run.

## CI relay

When running under GitHub Actions (`GITHUB_ACTIONS=true` **and** `GITHUB_OUTPUT`
set), `fullsend run` appends the outputs to `$GITHUB_OUTPUT` on every path it
reaches the skip decision on — skip, proceed, and harnesses that define no
`pre_script` at all — with `skipped` normalized to `true`/`false`. The composite
action re-exports them as the `skipped` and `skip-reason` action outputs.

A consumer can therefore distinguish three states:

| `steps.<id>.outputs.skipped` | Meaning |
|------------------------------|---------|
| `true` | The pre-script requested a skip. |
| `false` | The skip decision was made and was "proceed" — either the pre-script declined to skip, or the harness has no `pre_script`. |
| *(empty)* | The CLI predates this protocol, **or** the run failed before reaching the skip decision. |

The empty case is ambiguous by construction: a relay only happens once the
decision exists, so a run that fails earlier (harness load, pre-script failure,
malformed output) writes nothing. Check the step's `outcome` before treating an
empty `skipped` as a version signal.

If a relay target exists but writing fails, the run fails — a workflow gate
that disagrees with the CLI's decision is the failure mode this protocol is
meant to remove.

Other CI systems get the outputs in the run log only. GitLab `dotenv` report
artifacts are the natural equivalent and are not implemented in v1; GitLab
still gets the skip itself and the ⏭️ status comment, since both flow through
`fullsend run` and `forge.Client`.

## Version skew

Same class of concern as [ADR 0062](../../../ADRs/0062-dispatch-version-skew.md).
The two directions are handled asymmetrically, deliberately:

| Direction | Behavior |
|-----------|----------|
| Old script, new CLI | The script never writes to the file. Empty file → proceed. Fails **safe**. |
| New script, old CLI | `FULLSEND_PRESCRIPT_OUTPUT` is unset, so the script cannot signal a skip and the agent runs anyway. Fails **open** — a duplicate run. |

Scripts must therefore guard on the variable, the same pattern as the existing
`GITHUB_OUTPUT` guard in the scaffold scripts:

```sh
if [ -n "${FULLSEND_PRESCRIPT_OUTPUT:-}" ]; then
  {
    echo "skipped=true"
    echo "reason=an open PR already addresses this issue"
  } >> "${FULLSEND_PRESCRIPT_OUTPUT}"
fi
```

Without the guard, `>>` to an empty path fails the script and therefore the run.

## Versioning

Breaking changes require `docs/normative/prescript-output/v2/`.

| Change | v1 impact |
|--------|-----------|
| **Breaking** (requires v2): rename or remove a reserved key, change the meaning of a reserved value, tighten the grammar so previously valid files are rejected | Pre-scripts must migrate |
| **Non-breaking** (allowed in v1): add a reserved key, relax the grammar, clarify documentation | Existing pre-scripts keep working |

Adding a reserved key is non-breaking for the CLI but fails open on older CLIs,
which ignore it. Direct a script that depends on a newly added key at a CLI
version that understands it.
