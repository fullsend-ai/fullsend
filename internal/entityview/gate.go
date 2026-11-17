package entityview

import (
	"maps"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// entity applies the field classification to every member of e in place.
//
// Actor-originated actions (body, comments, reviews, label applications,
// reactions, current approvals) and singular actor-attributed values (title,
// lifecycle actors, thread resolvers, extension values) pass the actor gate.
// Current labels, assignees, and reviewers are relationships: their members
// are admitted through the action that produced them, never through the
// subject's own role. Raw source state (state, timestamps, checks, commits,
// changed files) is not an actor-originated action and is kept, subject only
// to source-revision checks.
func (b *builder) entity(e *normentity.Entity) {
	b.title(e)
	if a := e.Author.Ptr(); a != nil {
		*a = b.subject(*a)
	}
	b.lifecycleActor("lifecycle.closed_by", &e.Lifecycle.ClosedBy)
	b.lifecycleActor("lifecycle.merged_by", &e.Lifecycle.MergedBy)
	b.extensions(e.Extensions)
	b.body(e)

	comments := map[string]Outcome{}
	filterList(b, "comments", &e.Comments, func(c *normentity.Comment) (Outcome, string) {
		o, r := b.action(&c.Action)
		comments[c.ID] = worst(o, outcomeOr(comments, c.ID, Admitted))
		return o, r
	})
	filterList(b, "reviews", &e.Reviews, func(r *normentity.Review) (Outcome, string) {
		return b.action(&r.Action)
	})
	labels, labelReasons := map[string]Outcome{}, map[string]string{}
	filterList(b, "label_applications", &e.LabelApplications, func(l *normentity.LabelApplication) (Outcome, string) {
		o, r := b.action(&l.Action)
		if name, ok := l.Label.Value(); ok {
			if w := worst(o, outcomeOr(labels, name, Admitted)); w != labels[name] {
				labels[name], labelReasons[name] = w, r
			}
		}
		return o, r
	})
	// Current label state is recomputed from admitted label applications: a
	// label whose application is unauthorized or unattributed cannot enter
	// the view unchanged (Authorization Contract v2).
	filterList(b, "labels", &e.Labels, func(l *string) (Outcome, string) {
		o, ok := labels[*l]
		if !ok {
			return Unresolved, ReasonAttributionUnresolved
		}
		return o, labelReasons[*l]
	})
	filterList(b, "assignees", &e.Assignees, func(a *normentity.Actor) (Outcome, string) {
		return b.relationship(a, b.in.Provenance.Assigners)
	})
	filterList(b, "reviewers", &e.Reviewers, func(r *normentity.Reviewer) (Outcome, string) {
		return b.relationship(&r.Subject.Actor, b.in.Provenance.ReviewerAttribution)
	})
	filterList(b, "current_approvals", &e.CurrentApprovals, func(c *normentity.CurrentApproval) (Outcome, string) {
		return b.gatedActor(&c.Actor)
	})
	filterList(b, "reactions", &e.Reactions, func(r *normentity.Reaction) (Outcome, string) {
		return b.gatedActor(&r.Actor)
	})
	filterList(b, "threads", &e.Threads, nil)
	for i := range e.Threads.Known {
		b.thread(&e.Threads.Known[i], comments)
	}
	filterList(b, "checks", &e.Checks, nil)
	for i := range e.Checks.Known {
		b.extensions(e.Checks.Known[i].Extensions)
	}
	if cp := e.ChangeProposal.Ptr(); cp != nil {
		filterList(b, "changed_files", &cp.ChangedFiles, nil)
		filterList(b, "commits", &cp.Commits, nil)
		b.approval(cp)
	}
}

// title admits the title only when its current editor is admitted.
func (b *builder) title(e *normentity.Entity) {
	if _, ok := e.Title.Value(); !ok {
		return
	}
	o, r := Unresolved, ReasonAttributionUnresolved
	if ed := b.in.Provenance.TitleEditor; ed != nil {
		d := b.admit(*ed)
		o, r = d.outcome, d.reason
	}
	if o != Admitted {
		e.Title = normentity.Unknown[string](unknownReason(o))
	}
	b.field("title", o, r)
}

// body admits the body action or leaves it unknown.
func (b *builder) body(e *normentity.Entity) {
	body := e.Body.Ptr()
	if body == nil {
		return
	}
	o, r := b.action(&body.Action)
	if o != Admitted {
		e.Body = normentity.Unknown[normentity.TextAction](unknownReason(o))
	}
	b.field("body", o, r)
}

// lifecycleActor admits a known close or merge actor or leaves it unknown.
func (b *builder) lifecycleActor(name string, f *normentity.Field[normentity.Actor]) {
	a := f.Ptr()
	if a == nil {
		return
	}
	d := b.admit(*a)
	if d.outcome == Admitted {
		*a = withVerdict(*a, d)
	} else {
		*f = normentity.Unknown[normentity.Actor](unknownReason(d.outcome))
	}
	b.field(name, d.outcome, d.reason)
}

// approval keeps a raw approval gate only when it cannot be a positive
// signal. A shared document cannot have applied this harness's actor
// admission to the approvals behind "satisfied", so it becomes unknown.
func (b *builder) approval(cp *normentity.ChangeProposal) {
	ap := cp.Approval.Ptr()
	if ap == nil {
		return
	}
	if g, ok := ap.Gate.Value(); ok && g == normentity.GateSatisfied {
		ap.Gate = normentity.Unknown[normentity.ApprovalGate](normentity.ReasonPartial)
		b.field("change_proposal.approval.gate", Unresolved, ReasonApprovalNotRecomputed)
	}
}

// action gates an action record by its current-revision actor.
func (b *builder) action(a *normentity.Action) (Outcome, string) {
	o, r := b.gatedActor(&a.Actor)
	if o == Admitted {
		b.extensions(a.Extensions)
	}
	return o, r
}

// gatedActor gates a record by an actor that is responsible for it and, when
// admitted, replaces the actor's role fields with the verified evidence.
func (b *builder) gatedActor(a *normentity.Actor) (Outcome, string) {
	d := b.admit(*a)
	if d.outcome == Admitted {
		*a = withVerdict(*a, d)
	}
	return d.outcome, d.reason
}

// relationship admits a subject through the actor responsible for the
// relationship, looked up by the subject's source ID.
func (b *builder) relationship(subject *normentity.Actor, by map[string]normentity.Actor) (Outcome, string) {
	id, ok := subject.ID.Value()
	if !ok {
		return Unresolved, ReasonAttributionUnresolved
	}
	responsible, ok := by[id]
	if !ok {
		return Unresolved, ReasonAttributionUnresolved
	}
	d := b.admit(responsible)
	if d.outcome == Admitted {
		*subject = b.subject(*subject)
	}
	return d.outcome, d.reason
}

// thread filters a thread's comment IDs to admitted comments and gates its
// resolver.
func (b *builder) thread(t *normentity.Thread, comments map[string]Outcome) {
	counts := b.ev.Values["threads.comment_ids"]
	if !t.CommentIDs.IsZero() {
		kept := make([]string, 0, len(t.CommentIDs.Known))
		unresolved := false
		for _, id := range t.CommentIDs.Known {
			o := outcomeOr(comments, id, Unresolved)
			counts.add(o)
			switch o {
			case Admitted:
				kept = append(kept, id)
			case Unresolved:
				unresolved = true
			}
		}
		t.CommentIDs.Known = kept
		if unresolved {
			downgrade(&t.CommentIDs.Coverage, ReasonAttributionUnresolved)
		}
	}
	b.ev.Values["threads.comment_ids"] = counts

	if a := t.ResolvedBy.Ptr(); a != nil {
		d := b.admit(*a)
		if d.outcome == Admitted {
			*a = withVerdict(*a, d)
		} else {
			t.ResolvedBy = normentity.Unknown[normentity.Actor](unknownReason(d.outcome))
		}
		b.value("threads.resolved_by", d.outcome)
	}
	b.extensions(t.Extensions)
}

// extensions admits each extension value through the actor of its current
// revision, never through the containing record's actor.
func (b *builder) extensions(x *normentity.Extensions) {
	if x == nil {
		return
	}
	for _, m := range []map[string]normentity.ExtensionEntry{x.CustomFields, x.Properties} {
		for _, alias := range slices.Sorted(maps.Keys(m)) {
			entry := m[alias]
			o := Unresolved
			if a := entry.ChangedBy.Ptr(); a != nil {
				d := b.admit(*a)
				o = d.outcome
				if o == Admitted {
					*a = withVerdict(*a, d)
				}
			}
			if o == Admitted {
				if v := entry.Value.Ptr(); v != nil {
					b.extensionActors(v)
				}
			} else {
				if !entry.ChangedBy.IsZero() {
					entry.ChangedBy = normentity.Unknown[normentity.Actor](unknownReason(o))
				}
				if _, known := entry.Value.Value(); known || entry.Value.IsNull() {
					entry.Value = normentity.Unknown[normentity.ExtensionValue](unknownReason(o))
				}
			}
			m[alias] = entry
			b.value("extensions", o)
		}
	}
}

// extensionActors projects actor-valued extension atoms as subjects.
func (b *builder) extensionActors(v *normentity.ExtensionValue) {
	if v.Atom != nil && v.Atom.Actor != nil {
		*v.Atom.Actor = b.subject(*v.Atom.Actor)
	}
	if v.List != nil {
		for i := range v.List.Known {
			if a := v.List.Known[i].Actor; a != nil {
				*a = b.subject(*a)
			}
		}
	}
}

// filterList applies keep to every known member of an entity-level
// collection. Admitted members stay; excluded members are dropped with the
// coverage unchanged; unresolved members are dropped and make the coverage
// non-complete. A stale collection loses every known member. A nil keep
// retains members subject only to the source-revision checks.
func filterList[T any](b *builder, name string, l *normentity.List[T], keep func(*T) (Outcome, string)) {
	if l.IsZero() {
		return
	}
	ev := CollectionEvidence{InputCoverage: l.Coverage.Status}
	kept := make([]T, 0, len(l.Known))
	reason := ""
	switch {
	case b.stale(l.Coverage):
		reason = ReasonStaleSourceRevision
		if keep != nil {
			ev.Unresolved = len(l.Known)
		}
	default:
		for i := range l.Known {
			m := l.Known[i]
			if keep == nil {
				kept = append(kept, m)
				continue
			}
			o, r := keep(&m)
			ev.add(o)
			if o == Admitted {
				kept = append(kept, m)
			} else if o == Unresolved && reason == "" {
				reason = r
			}
		}
		if reason == "" && b.unbound(l.Coverage) {
			reason = ReasonUnboundRevision
		}
	}
	l.Known = kept
	if reason != "" {
		downgrade(&l.Coverage, reason)
	}
	ev.ViewCoverage = l.Coverage.Status
	ev.Reason = reason
	b.ev.Collections[name] = ev
	b.ev.Totals.Admitted += ev.Admitted
	b.ev.Totals.Excluded += ev.Excluded
	b.ev.Totals.Unresolved += ev.Unresolved
}

// stale reports whether coverage was recorded at a different source revision
// than the one established for this check.
func (b *builder) stale(c normentity.Coverage) bool {
	rev := b.in.Provenance.SourceRevision
	return rev != "" && c.SourceRevision != "" && c.SourceRevision != rev
}

// unbound reports whether a complete coverage cannot be tied to the source
// revision established for this check.
func (b *builder) unbound(c normentity.Coverage) bool {
	return b.in.Provenance.SourceRevision != "" && c.Status == normentity.CoverageComplete && c.SourceRevision == ""
}

func (b *builder) field(name string, o Outcome, reason string) {
	b.ev.Fields[name] = FieldEvidence{Outcome: o, Reason: reason}
	b.ev.Totals.add(o)
}

func (b *builder) value(name string, o Outcome) {
	c := b.ev.Values[name]
	c.add(o)
	b.ev.Values[name] = c
	b.ev.Totals.add(o)
}

// downgrade makes a complete or unrecorded coverage unknown. A coverage that
// is already non-complete keeps its more specific status.
func downgrade(c *normentity.Coverage, reason string) {
	if c.Status == normentity.CoverageComplete || c.Status == "" {
		c.Status = normentity.CoverageUnknown
		c.Reason = reason
	}
}

// unknownReason maps an outcome to the unknown-marker reason bound for a
// singular value: unresolved evidence failed, while an excluded value is a
// partial view of the source.
func unknownReason(o Outcome) normentity.UnknownReason {
	if o == Excluded {
		return normentity.ReasonPartial
	}
	return normentity.ReasonFailed
}

func outcomeOr(m map[string]Outcome, k string, def Outcome) Outcome {
	if o, ok := m[k]; ok {
		return o
	}
	return def
}
