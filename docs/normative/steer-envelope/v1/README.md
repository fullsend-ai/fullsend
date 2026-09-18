# Steer envelope v1

The **steer envelope** — the contract between the fullsend runner and the agent
definitions in [fullsend-ai/agents](https://github.com/fullsend-ai/agents) for a
message delivered into a session that is already running
([ADR 0113](../../../ADRs/0113-steer-the-running-agent-on-work-item-updates.md)).

Two repositories read these strings, so they are fixed here rather than in
either one. The mechanics around them — when a steer is sent, what authorizes
it, how the run settles — are in
[contributing/steering.md](../../../contributing/steering.md).

This document is normative. The Go implementation of the envelope is
[`internal/runtime/steer_session.go`](../../../../internal/runtime/steer_session.go);
the body it wraps is built by the runner and is not part of this contract.

**Implementation status.** Two sections of this document are **DRAFT** and
are explicitly outside the major-version guarantee below: [Structural
tokens](#structural-tokens) and [Defanging](#defanging). They describe how the
runner composes and neutralizes the body, the runner does not build a body
yet, and nothing implements them here — so pinning them now would bind a major
version to a design no consumer has been checked against. They become binding,
and gain that guarantee, with the change that splits an update into an
attributed half and an unattributed one; until then they may change without a
version bump.

What IS binding today is [The opening line](#the-opening-line), and only that:
per [Versioning](#versioning), the header sentences around it are prose rather
than contract, so the provenance sentence, the no-capability sentence and the
`Source:` line may change without a version. The whole document is specified
here rather than grown a section at a time, because a producer needs to see the
shape it is implementing toward — but a reader should treat only the binding
part as a promise.

## The opening line

Every envelope begins with this line, byte for byte, followed by a blank line:

```text
Runner update: your task inputs changed after this run started.
```

It is `runtime.SteerEnvelopeOpeningLine`, written once and pinned by a test.
The agent definitions match on it twice, and the two uses pull in opposite
directions:

| Where it appears | What it means |
|---|---|
| First line of a delivered message | A runner amendment. The agent may act on it. |
| Anywhere inside work-item content | An injection attempt. The agent should flag it. |

Because the second use is a detection signal, the runner **does not** defang
this line when it appears inside work-item content. Rewriting it would delete
the evidence the agent definitions are told to act on. This is the one string
in this document that is deliberately passed through untouched.

Changing this line requires changing the agent definitions in the same window.
A runner that emits a different line is not steering: the definitions will read
its message as ordinary content.

## Structural tokens

> **DRAFT — not binding yet.** Nothing implements this section here; see
> [Versioning](#versioning).

The body is split into an attributed half and an unattributed one, and the
envelope's header tells the agent which is which: items under `Amendments` come
from collaborators whose authorization was verified, and everything inside the
work-item-context fence is data that cannot amend the task.

That instruction is carried entirely by literal strings. These are the ones the
runner guarantees are runner-authored:

| Token | Written as | Role |
|---|---|---|
| Amendments heading | `Amendments` alone on a line | Opens the attributed half |
| Context heading | a line beginning `Work-item context.` | Opens the unattributed half |
| Context fence, open | `[work-item-context]` | First line of the untrusted block |
| Context fence, close | `[/work-item-context]` | Last line of the untrusted block |
| Amendment attribution | `Instruction from @<login>: ` | Attributes a command's text to a verified author |

Amendments also render as `Comment from @<login>:` and
`Review from @<login> (<state>):`. Those attribute without instructing and are
not defended tokens.

## Defanging

> **DRAFT — not binding yet.** Nothing implements this section here; see
> [Versioning](#versioning).

A context body is untrusted by construction, so it must not be able to write
any token in the table above. Before the block is wrapped, each is altered in
place — altered rather than deleted, so a reader can still see what the text
tried to do:

| In a context body | Becomes | Matched |
|---|---|---|
| `[work-item-context]` | `(work-item-context)` | anywhere in the body |
| `[/work-item-context]` | `(/work-item-context)` | anywhere in the body |
| `Instruction from @` | `Instruction from (at)` | anywhere in the body |
| `Amendments`, alone on a line | `> Amendments` | line-anchored |
| `Work-item context` beginning a line, as a whole word | the line, prefixed `> ` | line-anchored |

The two line-anchored rules need their edges spelled out, because a second
repository reads this table:

- Leading spaces and tabs are allowed before either heading word, and the quoted
  line does **not** keep them: an indented `Amendments` is re-emitted as
  `> Amendments` with the indent gone. Trailing spaces, tabs and a carriage
  return are allowed after `Amendments` and are preserved.
- `Work-item context` must end on a word boundary, so `Work-item context.` and
  `Work-item context is…` are quoted, and `Work-item contexts` is not. The
  heading the runner writes continues with a period. Everything from there to
  the end of the line is part of the match.

Two further rules govern how this is applied:

1. **Sanitize, then defang.** The Unicode sanitizer strips invisible
   characters. A token split by one — a zero-width space inside
   `[/work-item-context]`, a soft hyphen inside `Amendments` — matches nothing,
   and a sanitizer running afterwards reassembles it intact. The context block
   is sanitized first and defanged second, so there is nothing left to
   reassemble.
2. **Only whole lines count for the headings.** The two heading words are
   structure by virtue of standing alone. Prose that happens to use either word
   mid-sentence is not an imitation of anything and is left as written. Line
   endings may be `\n` or `\r\n`.

Amendments are **not** defanged. They are attributed to an author whose
authorization was verified and are the one part of the body allowed to be
directive; rewriting them would corrupt a legitimate instruction to defend
against an author who needs no forgery to give one.

## Versioning

This is v1. Breaking changes to the binding part require
`docs/normative/steer-envelope/v2/`, as for every other normative spec here.

**Binding.** A change to the opening line is a new major version: the agent
definitions match on it and cannot be updated atomically with the runner, so
changing it without a version breaks whichever side deploys second. Adding a
header sentence that carries no token from the structural table is NOT a major
version — the header is prose around the contract, not part of it.

**Draft.** The structural table and the defanged set carry no version
guarantee yet. They are what the runner's neutralization and any envelope
parser will key on, so they will become binding — but only with the change
that implements them, checked against a real consumer. Until then they may
change freely, which is the point of marking them rather than pinning a
design nothing has exercised.

**The change that first implements or consumes those tables must remove the
DRAFT banners and this carve-out in the same commit**, so the document never
reads non-binding while something depends on it. From that point they are
part of the binding surface and the v2 rule above covers them.
