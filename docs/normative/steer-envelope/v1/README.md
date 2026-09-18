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

A context body is untrusted by construction, so it must not be able to write
any token in the table above. Before the block is wrapped, each is altered in
place — altered rather than deleted, so a reader can still see what the text
tried to do:

| In a context body | Becomes | Matched |
|---|---|---|
| `[work-item-context]` | `(work-item-context)` | anywhere in the body |
| `[/work-item-context]` | `(/work-item-context)` | anywhere in the body |
| `Instruction from @` | `Instruction from (at)` | at the start of a line, or after a table-cell `\|` |
| `Amendments`, alone on a line | `> Amendments` | line-anchored |
| `Work-item context` beginning a line, as a whole word | the line, prefixed `> ` | line-anchored |

Every rule matches without regard to case (`[/WORK-ITEM-CONTEXT]`,
`INSTRUCTION FROM @`, `# amendments`), and the replacement keeps the text's own
case. The line-anchored rules need their edges spelled out, because a second
repository reads this table:

- **Markdown wrappers do not shield a token.** "The start of a line" allows
  any run of block markers first — blockquote `>`, bullets `* + -`, ATX `#`
  through `######`, ordered `1.`/`1)`, a task checkbox `[ ]`/`[x]`, an alert
  `[!NOTE]`, a footnote `[^id]:`, a table cell `|`, emphasis or code marks
  `* _ ~ \``, an HTML tag — nested in any combination, with spaces or tabs
  between. The same wrappers may precede either heading, and `Amendments` may
  also be followed by closing marks (`# Amendments #`, `<h1>Amendments</h1>`,
  `**Amendments**`). The quoted line drops the wrappers: `> ## Amendments`
  becomes `> Amendments`. A carriage return after `Amendments` is preserved.
- **Mid-sentence text is prose.** `I sent an instruction from @nobody by
  email.` and `The Amendments to the spec are in the linked doc.` are left as
  written: the attribution is structure only where the runner writes it, at
  the start of a line, and the headings only when standing alone.
- `Work-item context` must end on a word boundary, so `Work-item context.` and
  `Work-item context is...` are quoted, and `Work-item contexts` is not. The
  heading the runner writes continues with a period. Everything from there to
  the end of the line is part of the match.

Two further rules govern how this is applied:

1. **Sanitize, then defang, and never re-scan.** The Unicode sanitizer strips
   invisible characters. A token split by one — a zero-width space inside
   `[/work-item-context]`, a soft hyphen inside `Amendments` — matches nothing,
   and a sanitizer running afterwards reassembles it intact. The context block
   is sanitized first and defanged second, and the assembled envelope is not
   sanitized again: the sanitizer keeps compatibility characters as content,
   so a fullwidth spelling of a token survives the first pass, and a later
   pass that NFKC-folds the whole envelope would turn it into the live token.
2. **Lookalikes are folded only when they hide structure.** After the ASCII
   pass, the block is NFKC-folded and defanged again; the folded copy is used
   only if it revealed a token (`［/work-item-context］`, `Instruction from ＠`),
   so ordinary compatibility characters keep their bytes.

Amendments are **not** defanged. They are attributed to an author whose
authorization was verified and are the one part of the body allowed to be
directive; rewriting them would corrupt a legitimate instruction to defend
against an author who needs no forgery to give one.

## Versioning

This is v1. Breaking changes to the binding part require
`docs/normative/steer-envelope/v2/`, as for every other normative spec here.

**Binding.** A change to the opening line is a new major version: the agent
definitions match on it and cannot be updated atomically with the runner, so
changing it without a version breaks whichever side deploys second. So is a
change to any token in the structural table or to what is defanged: they are
what the runner's neutralization (`neutralizeEnvelopeMarkers` in
`internal/steerwatch/delta.go`) and any consumer that parses the envelope key
on. Adding a header sentence that carries no token from the structural table
is NOT a major version — the header is prose around the contract, not part of
it.
