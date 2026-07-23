# CEL Triggers and Normalized Events

Harness `trigger` expressions — and the CEL filters in `.feature` fixtures that
exercise them — match against the **normalized event model**, not raw
forge-specific webhook payloads. Whenever you write or review a CEL trigger, use
the normalized `transition.kind` vocabulary. A raw forge webhook action name
used where a `transition.kind` is expected silently evaluates to `false`, so the
trigger never fires and nothing warns you.

**Source of truth:**
[`docs/normative/normalized-event/v1/README.md`](../normative/normalized-event/v1/README.md).
Its [Transition kind vocabulary](../normative/normalized-event/v1/README.md#transition-kind-vocabulary),
[Transition sub-objects](../normative/normalized-event/v1/README.md#transition-sub-objects),
and [CEL trigger examples](../normative/normalized-event/v1/README.md#cel-trigger-examples)
sections are authoritative for the full enumerated kind list and the sub-object
each kind requires (`label`, `comment`, `review`, …). Do not re-copy that list
here — a duplicated table drifts from the schema.

**Highest-value pitfall — `synchronize` vs `synchronized`:** GitHub's raw
webhook action is `synchronize` (no trailing `d`), but the normalized kind is
**`synchronized`**. A CEL expression written as
`event.transition.kind == "synchronize"` silently never matches — this exact
typo appeared in a fork-dispatch scenario and was eliminated in PR #5309. The
same raw→normalized gap exists elsewhere (e.g. `labeled` / `unlabeled` →
`label_changed`); when unsure, check the vocabulary section above rather than
guessing from the webhook name.

**When reviewing** harness triggers or `.feature` CEL filters: flag any raw
forge webhook action name used where a normalized `transition.kind` is expected
(e.g. `"synchronize"`, `"labeled"`) as a **medium-severity** finding — it will
silently fail to match.
