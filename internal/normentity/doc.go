// Package normentity defines the in-memory Go model of the normalized entity
// (normalized-entity v1) that harness CEL predicates evaluate over
// (ADR 0098, ADR 0147). Contract: docs/normative/normalized-entity/v1/.
//
// The model is shaped for a CEL binding to wrap without copying:
//
//   - [Entity] mirrors the specification's `entity` object member for member,
//     and covers every field of the entity model, including the kind-specific
//     sections, reviewers and approvals, checks with log lines, annotations,
//     threads, reactions, and extensions.
//   - [Field] holds an attribute that can be unresolved: absent (does not
//     apply), known, null (source-confirmed no value), or unknown with a
//     reason. A binding maps unknown to CEL unknown at the attribute's path.
//   - [List] holds a collection's known members together with the coverage of
//     its remainder, which is what a coverage-aware CEL list needs. There is
//     deliberately no separate in-memory coverage structure: a parallel
//     structure would have to be rejoined to its collections on every
//     evaluation and could drift from them. The specification's host-only
//     `coverage` member exists only on the wire; [Document] splits it out when
//     marshaling and rejoins it when unmarshaling.
//   - [Document] pairs the entity with the host-only [View] and is the unit
//     that is serialized, validated against the JSON Schema, and cached.
//
// Timestamps stay strings so lexical comparison is chronological in CEL, as
// the specification requires. [ID] is the canonical entity ID.
package normentity
