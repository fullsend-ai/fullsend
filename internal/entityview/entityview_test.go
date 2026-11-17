package entityview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/actorcache"
	"github.com/fullsend-ai/fullsend/internal/entitycache"
	"github.com/fullsend-ai/fullsend/internal/normentity"
	"github.com/fullsend-ai/fullsend/internal/normevent"
)

const (
	host     = "github.com"
	revision = "rev-2"
)

var checkTime = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

// Actor source IDs used by the fixtures.
const (
	maintainer = "U_maintainer"
	writer     = "U_writer"
	triager    = "U_triager"
	reader     = "U_reader"
	unknownAct = "U_unknown"
	registered = "B_registered"
	unregBot   = "B_unregistered"
)

func human(id string) normentity.Actor {
	return normentity.Actor{
		ID:    normentity.Known(id),
		Login: normentity.Known(id),
		Kind:  normentity.Known(normentity.ActorHuman),
		// A stale claim in the shared document that the view must not
		// trust.
		Role:         normentity.Known(normentity.RoleAdmin),
		RoleVerified: normentity.Known(true),
	}
}

func bot(id string) normentity.Actor {
	return normentity.Actor{
		ID:           normentity.Known(id),
		Login:        normentity.Known(id + "[bot]"),
		Kind:         normentity.Known(normentity.ActorBot),
		Role:         normentity.Known(normentity.RoleNone),
		BotRole:      normentity.Known("claimed"),
		RoleVerified: normentity.Known(true),
	}
}

func actorID(t *testing.T, id string) actorcache.ActorID {
	t.Helper()
	a, err := actorcache.NewActorID("github", host, id)
	require.NoError(t, err)
	return a
}

// live returns fresh verdicts for every fixture actor except unknownAct.
func live(t *testing.T) map[actorcache.ActorID]Verdict {
	t.Helper()
	start := checkTime.Add(-10 * time.Second)
	h := func(r normentity.Role) Verdict {
		return Verdict{Kind: normentity.ActorHuman, Role: r, Verified: true, LookupStart: start, PermissionSource: "collaborators"}
	}
	return map[actorcache.ActorID]Verdict{
		actorID(t, maintainer): h(normentity.RoleMaintain),
		actorID(t, writer):     h(normentity.RoleWrite),
		actorID(t, triager):    h(normentity.RoleTriage),
		actorID(t, reader):     h(normentity.RoleRead),
		actorID(t, registered): {Kind: normentity.ActorBot, Role: normentity.RoleNone, BotRole: "triage", Verified: true, LookupStart: start, PermissionSource: "bot_registry"},
		actorID(t, unregBot):   {Kind: normentity.ActorBot, Role: normentity.RoleNone, Verified: true, LookupStart: start, PermissionSource: "bot_registry"},
	}
}

func complete() normentity.Coverage {
	return normentity.Coverage{Status: normentity.CoverageComplete, SourceRevision: revision}
}

func comment(id string, a normentity.Actor) normentity.Comment {
	return normentity.Comment{
		TextAction: normentity.TextAction{
			Action:  normentity.Action{ID: id, CreatedAt: "2026-11-17T10:00:00.000000000Z", Actor: a},
			Text:    normentity.Known("/fs-fix please"),
			Command: normentity.Known("/fs-fix"),
		},
		ThreadID: normentity.Known("T1"),
	}
}

func labelApp(id, label string, a normentity.Actor) normentity.LabelApplication {
	return normentity.LabelApplication{
		Action: normentity.Action{ID: id, CreatedAt: "2026-11-17T10:00:00.000000000Z", Actor: a},
		Label:  normentity.Known(label),
	}
}

// workItem returns a GitHub issue with complete collections touched by
// actors of every class.
func workItem(t *testing.T) *normentity.Document {
	t.Helper()
	id, err := normentity.NewID("github", host, "work_item", "I_1")
	require.NoError(t, err)
	closer := human(writer)
	return &normentity.Document{
		Entity: normentity.Entity{
			Kind:     normentity.KindWorkItem,
			Source:   normentity.Source{System: normentity.SystemGitHub, Host: host, Repository: "o/r", RepositoryID: "R_1"},
			ID:       id,
			SourceID: "I_1",
			Key:      "#1",
			Number:   1,
			URL:      "https://github.com/o/r/issues/1",
			State:    normentity.Known(normentity.StateOpen),
			Title:    normentity.Known("Fix the thing"),
			Author:   normentity.Known(human(reader)),
			Lifecycle: normentity.Lifecycle{
				ClosedAt: normentity.Null[normentity.Timestamp](),
				ClosedBy: normentity.Known(closer),
			},
			Body: normentity.Known(normentity.TextAction{
				Action: normentity.Action{ID: "I_1", CreatedAt: "2026-11-17T09:00:00.000000000Z", Actor: human(writer)},
				Text:   normentity.Known("body"),
			}),
			Comments: normentity.List[normentity.Comment]{
				Known: []normentity.Comment{
					comment("C_writer", human(writer)),
					comment("C_reader", human(reader)),
					comment("C_bot", bot(registered)),
					comment("C_unreg", bot(unregBot)),
				},
				Coverage: complete(),
			},
			LabelApplications: normentity.List[normentity.LabelApplication]{
				Known: []normentity.LabelApplication{
					labelApp("L1", "ready", human(triager)),
					labelApp("L2", "spam", human(reader)),
				},
				Coverage: complete(),
			},
			Labels: normentity.List[string]{Known: []string{"ready", "spam"}, Coverage: complete()},
			Assignees: normentity.List[normentity.Actor]{
				Known:    []normentity.Actor{human(reader)},
				Coverage: complete(),
			},
			Reactions: normentity.List[normentity.Reaction]{
				Known:    []normentity.Reaction{{ID: "R1", Actor: human(maintainer)}, {ID: "R2", Actor: human(reader)}},
				Coverage: complete(),
			},
			Threads: normentity.List[normentity.Thread]{
				Known: []normentity.Thread{{
					ID:         "T1",
					CreatedAt:  "2026-11-17T10:00:00.000000000Z",
					CommentIDs: normentity.List[string]{Known: []string{"C_writer", "C_reader", "C_bot"}, Coverage: complete()},
					ResolvedBy: normentity.Known(human(reader)),
				}},
				Coverage: complete(),
			},
			Extensions: &normentity.Extensions{
				CustomFields: map[string]normentity.ExtensionEntry{
					"priority": {
						SourceKey: "customfield_1",
						Value:     normentity.Known(normentity.ExtensionValue{Atom: &normentity.ExtensionAtom{String: new("high")}}),
						ChangedBy: normentity.Known(human(writer)),
					},
					"owner": {
						SourceKey: "customfield_2",
						Value:     normentity.Known(normentity.ExtensionValue{Atom: &normentity.ExtensionAtom{Actor: new(human(triager))}}),
						ChangedBy: normentity.Known(human(reader)),
					},
				},
			},
			WorkItem: normentity.Known(normentity.WorkItem{}),
		},
		View: normentity.View{HumanMinRole: normentity.RoleTriage, ResolvedAt: "2026-11-17T12:00:00.000000000Z", FilterVersion: "f1"},
	}
}

func input(t *testing.T, doc *normentity.Document, minRole normentity.Role) Input {
	t.Helper()
	editor := human(writer)
	return Input{
		Document: doc,
		Policy:   Policy{Identity: "policy-1", HumanMinRole: minRole},
		Provenance: Provenance{
			SourceRevision: revision,
			TitleEditor:    &editor,
			Assigners:      map[string]normentity.Actor{reader: human(writer)},
		},
		Roles: RoleEvidence{Live: live(t)},
		Now:   checkTime,
	}
}

func build(t *testing.T, in Input) *View {
	t.Helper()
	v, err := Build(in)
	require.NoError(t, err)
	return v
}

func commentIDs(e *normentity.Entity) []string {
	ids := []string{}
	for _, c := range e.Comments.Known {
		ids = append(ids, c.ID)
	}
	return ids
}

func TestAdmittedNestedActionKeepsPathAndVerifiedRole(t *testing.T) {
	v := build(t, input(t, workItem(t), normentity.RoleTriage))
	e := v.Document.Entity

	// The writer's and registered bot's comments stay at their paths; the
	// reader (below triage) and unregistered bot are excluded.
	assert.Equal(t, []string{"C_writer", "C_bot"}, commentIDs(&e))
	assert.Equal(t, normentity.CoverageComplete, e.Comments.Coverage.Status,
		"known exclusions keep the authorized view complete")

	c := e.Comments.Known[0]
	assert.Equal(t, "C_writer", c.ID)
	cmd, _ := c.Command.Value()
	assert.Equal(t, "/fs-fix", cmd)
	role, _ := c.Actor.Role.Value()
	assert.Equal(t, normentity.RoleWrite, role, "the role comes from verified evidence, not the shared claim")
	assert.True(t, c.Actor.BotRole.IsZero(), "a human omits bot_role")

	b := e.Comments.Known[1].Actor
	botRole, _ := b.BotRole.Value()
	assert.Equal(t, "triage", botRole, "a bot carries its verified registered role, not its claimed one")

	ev := v.Evidence.Collections["comments"]
	assert.Equal(t, Counts{Admitted: 2, Excluded: 2}, ev.Counts)
	assert.Equal(t, normentity.CoverageComplete, ev.ViewCoverage)
	assert.Equal(t, Gate, v.Evidence.Gate)
	assert.Equal(t, normentity.RoleTriage, v.Document.View.HumanMinRole)
}

func TestActorBelowMinimumIsExcluded(t *testing.T) {
	v := build(t, input(t, workItem(t), normentity.RoleWrite))
	e := v.Document.Entity

	// Under a write minimum, the triager's label application and the
	// label it produced are excluded, as are triage-only reactions.
	assert.Empty(t, e.LabelApplications.Known)
	assert.Equal(t, normentity.CoverageComplete, e.LabelApplications.Coverage.Status)
	assert.Empty(t, e.Labels.Known)
	assert.Equal(t, normentity.CoverageComplete, e.Labels.Coverage.Status)
	assert.Equal(t, Counts{Excluded: 2}, v.Evidence.Collections["labels"].Counts)

	require.Len(t, e.Reactions.Known, 1)
	assert.Equal(t, "R1", e.Reactions.Known[0].ID)

	var triage *ActorEvidence
	for i := range v.Evidence.Actors {
		if v.Evidence.Actors[i].Actor == actorID(t, triager) {
			triage = &v.Evidence.Actors[i]
		}
	}
	require.NotNil(t, triage)
	assert.Equal(t, Excluded, triage.Outcome)
	assert.Equal(t, ReasonBelowMinimum, triage.Reason)
	assert.Equal(t, SourceLive, triage.Source)
	assert.Equal(t, 10*time.Second, triage.Age)
}

func TestLabelsAreRecomputedFromAdmittedApplications(t *testing.T) {
	v := build(t, input(t, workItem(t), normentity.RoleTriage))
	e := v.Document.Entity
	assert.Equal(t, []string{"ready"}, e.Labels.Known, "the reader-applied label cannot enter the view")
	assert.Equal(t, normentity.CoverageComplete, e.Labels.Coverage.Status)

	// A label with no attributable application is unresolved.
	doc := workItem(t)
	doc.Entity.Labels.Known = append(doc.Entity.Labels.Known, "orphan")
	v = build(t, input(t, doc, normentity.RoleTriage))
	assert.Equal(t, []string{"ready"}, v.Document.Entity.Labels.Known)
	assert.Equal(t, normentity.CoverageUnknown, v.Document.Entity.Labels.Coverage.Status)
	assert.Equal(t, ReasonAttributionUnresolved, v.Document.Entity.Labels.Coverage.Reason)
}

func TestUnresolvedActorMakesCoverageNonComplete(t *testing.T) {
	doc := workItem(t)
	noID := human("")
	noID.ID = normentity.Unknown[string](normentity.ReasonNotLoaded)
	doc.Entity.Comments.Known = append(doc.Entity.Comments.Known,
		comment("C_unknown", human(unknownAct)), // no role evidence
		comment("C_noid", noID),                 // no attribution
	)
	v := build(t, input(t, doc, normentity.RoleTriage))
	e := v.Document.Entity

	assert.Equal(t, []string{"C_writer", "C_bot"}, commentIDs(&e))
	assert.Equal(t, normentity.CoverageUnknown, e.Comments.Coverage.Status,
		"unresolved records must not leave a complete collection")
	assert.Equal(t, ReasonRoleUnresolved, e.Comments.Coverage.Reason)
	assert.Equal(t, revision, e.Comments.Coverage.SourceRevision)
	assert.Equal(t, Counts{Admitted: 2, Excluded: 2, Unresolved: 2}, v.Evidence.Collections["comments"].Counts)

	var unk *ActorEvidence
	for i := range v.Evidence.Actors {
		if v.Evidence.Actors[i].Actor == actorID(t, unknownAct) {
			unk = &v.Evidence.Actors[i]
		}
	}
	require.NotNil(t, unk)
	assert.Equal(t, SourceNone, unk.Source)
	assert.Equal(t, Unresolved, unk.Outcome)
}

func TestUnresolvedOnlyRecordLeavesNoEmptyCompleteCollection(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Reactions.Known = []normentity.Reaction{{ID: "R9", Actor: human(unknownAct)}}
	v := build(t, input(t, doc, normentity.RoleTriage))
	r := v.Document.Entity.Reactions
	assert.Empty(t, r.Known)
	assert.False(t, r.IsZero(), "the collection stays present")
	assert.NotEqual(t, normentity.CoverageComplete, r.Coverage.Status,
		"an empty list must not claim conclusive absence")
}

func TestStaleEvidence(t *testing.T) {
	t.Run("stale live verdict is unusable", func(t *testing.T) {
		in := input(t, workItem(t), normentity.RoleTriage)
		v := in.Roles.Live[actorID(t, writer)]
		v.LookupStart = checkTime.Add(-actorcache.HardMaxAge)
		in.Roles.Live[actorID(t, writer)] = v
		view := build(t, in)
		e := view.Document.Entity
		assert.Equal(t, []string{"C_bot"}, commentIDs(&e))
		assert.Equal(t, normentity.CoverageUnknown, e.Comments.Coverage.Status)
		_, known := e.Body.Value()
		assert.False(t, known, "a body whose editor's evidence is stale is unknown")
	})
	t.Run("future lookup start is unusable", func(t *testing.T) {
		in := input(t, workItem(t), normentity.RoleTriage)
		v := in.Roles.Live[actorID(t, writer)]
		v.LookupStart = checkTime.Add(time.Second)
		in.Roles.Live[actorID(t, writer)] = v
		view := build(t, in)
		assert.Equal(t, normentity.CoverageUnknown, view.Document.Entity.Comments.Coverage.Status)
	})
	t.Run("unverified or mismatched verdict is unusable", func(t *testing.T) {
		in := input(t, workItem(t), normentity.RoleTriage)
		v := in.Roles.Live[actorID(t, writer)]
		v.Verified = false
		in.Roles.Live[actorID(t, writer)] = v
		bv := in.Roles.Live[actorID(t, registered)]
		bv.Kind = normentity.ActorHuman
		in.Roles.Live[actorID(t, registered)] = bv
		view := build(t, in)
		e := view.Document.Entity
		assert.Empty(t, commentIDs(&e))
		assert.Equal(t, normentity.CoverageUnknown, e.Comments.Coverage.Status)
	})
	t.Run("stale source revision drops witnesses", func(t *testing.T) {
		doc := workItem(t)
		doc.Entity.Comments.Coverage.SourceRevision = "rev-1"
		view := build(t, input(t, doc, normentity.RoleTriage))
		e := view.Document.Entity
		assert.Empty(t, e.Comments.Known)
		assert.Equal(t, normentity.CoverageUnknown, e.Comments.Coverage.Status)
		assert.Equal(t, ReasonStaleSourceRevision, e.Comments.Coverage.Reason)
		assert.Equal(t, Counts{Unresolved: 4}, view.Evidence.Collections["comments"].Counts)

		// Thread membership of stale comments is unresolved, not empty.
		th := e.Threads.Known[0]
		assert.Empty(t, th.CommentIDs.Known)
		assert.Equal(t, normentity.CoverageUnknown, th.CommentIDs.Coverage.Status)
	})
	t.Run("complete coverage without a revision is not complete at this revision", func(t *testing.T) {
		doc := workItem(t)
		doc.Entity.Reactions.Coverage.SourceRevision = ""
		view := build(t, input(t, doc, normentity.RoleTriage))
		r := view.Document.Entity.Reactions
		assert.Len(t, r.Known, 1, "admitted witnesses stay")
		assert.Equal(t, normentity.CoverageUnknown, r.Coverage.Status)
		assert.Equal(t, ReasonUnboundRevision, r.Coverage.Reason)
	})
	t.Run("stale raw-state collection", func(t *testing.T) {
		doc := changeProposal(t)
		cp := doc.Entity.ChangeProposal.Ptr()
		cp.Commits.Coverage.SourceRevision = "rev-1"
		view := build(t, input(t, doc, normentity.RoleTriage))
		got := view.Document.Entity.ChangeProposal.Ptr()
		assert.Empty(t, got.Commits.Known)
		assert.Equal(t, normentity.CoverageUnknown, got.Commits.Coverage.Status)
		assert.Equal(t, []string{"sha1"}, func() []string {
			out := []string{}
			for _, f := range got.ChangedFiles.Known {
				out = append(out, f.Path)
			}
			return out
		}(), "other collections are unaffected")
	})
}

func TestIncompleteSourceCoverageIsPreserved(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Comments.Coverage = normentity.Coverage{Status: normentity.CoveragePartial, Reason: "pagination_incomplete"}
	doc.Entity.Reactions.Coverage = normentity.Coverage{Status: normentity.CoverageFailed}
	v := build(t, input(t, doc, normentity.RoleTriage))
	e := v.Document.Entity
	assert.Equal(t, normentity.CoveragePartial, e.Comments.Coverage.Status)
	assert.Equal(t, "pagination_incomplete", e.Comments.Coverage.Reason)
	assert.Equal(t, normentity.CoverageFailed, e.Reactions.Coverage.Status)
	assert.Equal(t, normentity.CoveragePartial, v.Evidence.Collections["comments"].ViewCoverage)

	// An unresolved member does not upgrade or rewrite a more specific
	// non-complete status.
	doc.Entity.Comments.Known = append(doc.Entity.Comments.Known, comment("C_u", human(unknownAct)))
	v = build(t, input(t, doc, normentity.RoleTriage))
	assert.Equal(t, normentity.CoveragePartial, v.Document.Entity.Comments.Coverage.Status)
}

func TestSingularFieldsUnknownWhenEditorNotAdmitted(t *testing.T) {
	cases := []struct {
		name   string
		editor *normentity.Actor
		reason normentity.UnknownReason
		out    Outcome
	}{
		{"unauthorized editor", new(human(reader)), normentity.ReasonPartial, Excluded},
		{"unresolved editor role", new(human(unknownAct)), normentity.ReasonFailed, Unresolved},
		{"missing editor attribution", nil, normentity.ReasonFailed, Unresolved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(t, workItem(t), normentity.RoleTriage)
			in.Provenance.TitleEditor = tc.editor
			v := build(t, in)
			r, unknown := v.Document.Entity.Title.UnknownReason()
			assert.True(t, unknown, "the title stays unknown, not empty")
			assert.Equal(t, tc.reason, r)
			assert.Equal(t, tc.out, v.Evidence.Fields["title"].Outcome)
		})
	}

	t.Run("author does not substitute for the title editor", func(t *testing.T) {
		doc := workItem(t)
		doc.Entity.Author = normentity.Known(human(maintainer))
		in := input(t, doc, normentity.RoleTriage)
		in.Provenance.TitleEditor = nil
		v := build(t, in)
		_, unknown := v.Document.Entity.Title.UnknownReason()
		assert.True(t, unknown)
	})

	for _, tc := range []struct {
		name  string
		actor normentity.Actor
		out   Outcome
	}{
		{"unauthorized body editor", human(reader), Excluded},
		{"unresolved body editor", human(unknownAct), Unresolved},
		{"unregistered bot body editor", bot(unregBot), Excluded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := workItem(t)
			doc.Entity.Body.Ptr().Actor = tc.actor
			v := build(t, input(t, doc, normentity.RoleTriage))
			_, unknown := v.Document.Entity.Body.UnknownReason()
			assert.True(t, unknown, "the body stays unknown, never null")
			assert.False(t, v.Document.Entity.Body.IsNull())
			assert.Equal(t, tc.out, v.Evidence.Fields["body"].Outcome)
		})
	}

	t.Run("admitted editors keep title and body", func(t *testing.T) {
		v := build(t, input(t, workItem(t), normentity.RoleTriage))
		title, ok := v.Document.Entity.Title.Value()
		assert.True(t, ok)
		assert.Equal(t, "Fix the thing", title)
		body := v.Document.Entity.Body.Ptr()
		require.NotNil(t, body)
		role, _ := body.Actor.Role.Value()
		assert.Equal(t, normentity.RoleWrite, role)
	})
}

func TestNestedSingularValues(t *testing.T) {
	v := build(t, input(t, workItem(t), normentity.RoleTriage))
	e := v.Document.Entity

	// The reader resolved the thread: the resolver is unknown, not null.
	th := e.Threads.Known[0]
	_, unknown := th.ResolvedBy.UnknownReason()
	assert.True(t, unknown)
	assert.Equal(t, Counts{Excluded: 1}, v.Evidence.Values["threads.resolved_by"])

	// Comment IDs keep only admitted comments; the reader's known exclusion
	// keeps the thread's coverage complete.
	assert.Equal(t, []string{"C_writer", "C_bot"}, th.CommentIDs.Known)
	assert.Equal(t, normentity.CoverageComplete, th.CommentIDs.Coverage.Status)

	// The closer is admitted with the verified role.
	closer := e.Lifecycle.ClosedBy.Ptr()
	require.NotNil(t, closer)
	role, _ := closer.Role.Value()
	assert.Equal(t, normentity.RoleWrite, role)
	assert.True(t, e.Lifecycle.ClosedAt.IsNull(), "raw lifecycle state is kept")

	// Extension values follow their own changer, not the entity actor.
	prio := e.Extensions.CustomFields["priority"]
	_, known := prio.Value.Value()
	assert.True(t, known)
	owner := e.Extensions.CustomFields["owner"]
	_, unknown = owner.Value.UnknownReason()
	assert.True(t, unknown, "a value changed by an unauthorized actor is unknown")
	_, unknown = owner.ChangedBy.UnknownReason()
	assert.True(t, unknown)
	assert.Equal(t, Counts{Admitted: 1, Excluded: 1}, v.Evidence.Values["extensions"])

	// The author is a subject: kept, with verified role evidence.
	author := e.Author.Ptr()
	require.NotNil(t, author)
	role, _ = author.Role.Value()
	assert.Equal(t, normentity.RoleRead, role)
}

func TestLifecycleActorNotAdmitted(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Lifecycle.ClosedBy = normentity.Known(human(unknownAct))
	v := build(t, input(t, doc, normentity.RoleTriage))
	_, unknown := v.Document.Entity.Lifecycle.ClosedBy.UnknownReason()
	assert.True(t, unknown)
	assert.Equal(t, Unresolved, v.Evidence.Fields["lifecycle.closed_by"].Outcome)
}

func TestRelationshipsUseResponsibleActor(t *testing.T) {
	t.Run("admitted assigner keeps a below-minimum assignee", func(t *testing.T) {
		v := build(t, input(t, workItem(t), normentity.RoleTriage))
		a := v.Document.Entity.Assignees
		require.Len(t, a.Known, 1)
		role, _ := a.Known[0].Role.Value()
		assert.Equal(t, normentity.RoleRead, role, "the assignee's own role does not gate the assignment")
	})
	t.Run("unauthorized assigner excludes the assignment", func(t *testing.T) {
		in := input(t, workItem(t), normentity.RoleTriage)
		in.Provenance.Assigners = map[string]normentity.Actor{reader: human(reader)}
		v := build(t, in)
		assert.Empty(t, v.Document.Entity.Assignees.Known)
		assert.Equal(t, normentity.CoverageComplete, v.Document.Entity.Assignees.Coverage.Status)
	})
	t.Run("missing assignment provenance is unresolved", func(t *testing.T) {
		in := input(t, workItem(t), normentity.RoleTriage)
		in.Provenance.Assigners = nil
		v := build(t, in)
		assert.Empty(t, v.Document.Entity.Assignees.Known)
		assert.Equal(t, normentity.CoverageUnknown, v.Document.Entity.Assignees.Coverage.Status)
	})
}

func changeProposal(t *testing.T) *normentity.Document {
	t.Helper()
	id, err := normentity.NewID("github", host, "change_proposal", "PR_1")
	require.NoError(t, err)
	team := normentity.ReviewerSubject{
		Actor: normentity.Actor{ID: normentity.Known("TEAM_1"), Kind: normentity.Known(normentity.ActorTeam)},
		Name:  normentity.Known("core"),
	}
	return &normentity.Document{
		Entity: normentity.Entity{
			Kind:     normentity.KindChangeProposal,
			Source:   normentity.Source{System: normentity.SystemGitHub, Host: host, Repository: "o/r", RepositoryID: "R_1"},
			ID:       id,
			SourceID: "PR_1",
			Key:      "#2",
			Number:   2,
			URL:      "https://github.com/o/r/pull/2",
			Reviews: normentity.List[normentity.Review]{
				Known: []normentity.Review{
					{TextAction: normentity.TextAction{Action: normentity.Action{ID: "RV1", CreatedAt: "2026-11-17T10:00:00.000000000Z", Actor: human(maintainer)}}},
					{TextAction: normentity.TextAction{Action: normentity.Action{ID: "RV2", CreatedAt: "2026-11-17T10:00:00.000000000Z", Actor: human(triager)}}},
				},
				Coverage: complete(),
			},
			Reviewers: normentity.List[normentity.Reviewer]{
				Known:    []normentity.Reviewer{{Subject: team}, {Subject: normentity.ReviewerSubject{Actor: human(reader)}}},
				Coverage: complete(),
			},
			CurrentApprovals: normentity.List[normentity.CurrentApproval]{
				Known:    []normentity.CurrentApproval{{Actor: human(maintainer)}, {Actor: human(triager)}},
				Coverage: complete(),
			},
			Checks: normentity.List[normentity.Check]{
				Known:    []normentity.Check{{ID: "CK1", CreatedAt: "2026-11-17T10:00:00.000000000Z", HeadSHA: "sha1"}},
				Coverage: complete(),
			},
			ChangeProposal: normentity.Known(normentity.ChangeProposal{
				Approval:     normentity.Known(normentity.Approval{Gate: normentity.Known(normentity.GateSatisfied)}),
				ChangedFiles: normentity.List[normentity.ChangedFile]{Known: []normentity.ChangedFile{{Path: "sha1"}}, Coverage: complete()},
				Commits:      normentity.List[string]{Known: []string{"sha1"}, Coverage: complete()},
			}),
		},
		View: normentity.View{HumanMinRole: normentity.RoleTriage, ResolvedAt: "2026-11-17T12:00:00.000000000Z", FilterVersion: "f1"},
	}
}

func TestChangeProposalCollections(t *testing.T) {
	in := input(t, changeProposal(t), normentity.RoleWrite)
	in.Provenance.ReviewerAttribution = map[string]normentity.Actor{
		"TEAM_1": human(maintainer),
		reader:   human(triager),
	}
	v := build(t, in)
	e := v.Document.Entity

	require.Len(t, e.Reviews.Known, 1)
	assert.Equal(t, "RV1", e.Reviews.Known[0].ID)
	require.Len(t, e.CurrentApprovals.Known, 1)
	assert.Equal(t, normentity.CoverageComplete, e.CurrentApprovals.Coverage.Status)

	// The team request by a maintainer stays unchanged; the triager's
	// request is below the write minimum.
	require.Len(t, e.Reviewers.Known, 1)
	name, _ := e.Reviewers.Known[0].Subject.Name.Value()
	assert.Equal(t, "core", name)
	assert.True(t, e.Reviewers.Known[0].Subject.Role.IsZero(), "team subjects gain no role")

	// Checks, files, and commits are raw state, not actor actions.
	assert.Len(t, e.Checks.Known, 1)
	cp := e.ChangeProposal.Ptr()
	assert.Len(t, cp.Commits.Known, 1)
	assert.Equal(t, Counts{}, v.Evidence.Collections["checks"].Counts)

	// A shared "satisfied" gate cannot be a positive signal for this harness.
	_, unknown := cp.Approval.Ptr().Gate.UnknownReason()
	assert.True(t, unknown)
	assert.Equal(t, ReasonApprovalNotRecomputed, v.Evidence.Fields["change_proposal.approval.gate"].Reason)
}

func TestApprovalGateUnsatisfiedIsKept(t *testing.T) {
	doc := changeProposal(t)
	doc.Entity.ChangeProposal.Ptr().Approval = normentity.Known(normentity.Approval{Gate: normentity.Known(normentity.GateUnsatisfied)})
	v := build(t, input(t, doc, normentity.RoleTriage))
	g, ok := v.Document.Entity.ChangeProposal.Ptr().Approval.Ptr().Gate.Value()
	assert.True(t, ok)
	assert.Equal(t, normentity.GateUnsatisfied, g)
}

func marshal(t *testing.T, d *normentity.Document) string {
	t.Helper()
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	return string(raw)
}

func TestIndependentViewsDoNotMutateSharedEntityOrCache(t *testing.T) {
	cache, err := entitycache.NewMemory(entitycache.MemoryOptions{})
	require.NoError(t, err)
	doc := workItem(t)
	rev, err := cache.Put(doc.Entity.ID, doc, 0)
	require.NoError(t, err)

	shared, gotRev, ok := cache.Get(doc.Entity.ID)
	require.True(t, ok)
	require.Equal(t, rev, gotRev)
	before := marshal(t, shared)

	triageView := build(t, input(t, shared, normentity.RoleTriage))
	writeView := build(t, input(t, shared, normentity.RoleWrite))
	maintainView := build(t, input(t, shared, normentity.RoleMaintain))

	assert.Equal(t, before, marshal(t, shared), "the shared document is unchanged")
	cached, cachedRev, ok := cache.Get(doc.Entity.ID)
	require.True(t, ok)
	assert.Equal(t, rev, cachedRev, "views are never written back to the cache")
	assert.Equal(t, before, marshal(t, cached))

	ids := func(v *View) []string { return commentIDs(&v.Document.Entity) }
	assert.Equal(t, []string{"C_writer", "C_bot"}, ids(triageView))
	assert.Equal(t, []string{"C_writer", "C_bot"}, ids(writeView))
	assert.Equal(t, []string{"C_bot"}, ids(maintainView), "a registered bot is not subject to the human minimum")
	assert.Equal(t, []string{"ready"}, triageView.Document.Entity.Labels.Known)
	assert.Empty(t, writeView.Document.Entity.Labels.Known)

	assert.Equal(t, normentity.RoleTriage, triageView.Document.View.HumanMinRole)
	assert.Equal(t, normentity.RoleMaintain, maintainView.Document.View.HumanMinRole)

	// Mutating one view does not affect another or the shared document.
	triageView.Document.Entity.Comments.Known[0].ID = "mutated"
	assert.Equal(t, "C_writer", writeView.Document.Entity.Comments.Known[0].ID)
	assert.Equal(t, before, marshal(t, shared))
}

func cacheKey(t *testing.T, id string, kind normevent.ActorKind, s CacheScope) actorcache.Key {
	t.Helper()
	return actorcache.Key{
		Actor: actorID(t, id), Kind: kind, Tenant: s.Tenant, Repository: s.Repository,
		Policy: s.Policy, PolicyRevision: s.PolicyRevision, PermissionSource: s.PermissionSource,
		Ref: s.Ref, SourceRevision: s.SourceRevision,
	}
}

// fakeCache serves fixed entries without enforcing expiry, so the view's own
// age enforcement is observable.
type fakeCache struct {
	entries map[actorcache.Key]actorcache.Entry
	gets    int
}

func (f *fakeCache) Get(k actorcache.Key) (actorcache.Entry, bool) {
	f.gets++
	e, ok := f.entries[k]
	return e, ok
}
func (f *fakeCache) Put(actorcache.Key, actorcache.Entry) error { return nil }
func (f *fakeCache) Invalidate(actorcache.ActorID)              {}

func TestActorRoleCacheReuse(t *testing.T) {
	humanScope := CacheScope{Tenant: "o", Repository: "R_1", Policy: "authz", PolicyRevision: "p1", PermissionSource: "collaborators"}
	botScope := CacheScope{Tenant: "o", Repository: "R_1", Policy: "authz", PolicyRevision: "p1", PermissionSource: "bot_registry", SourceRevision: "reg-1"}

	// onlyWriterComment returns a doc whose one comment's actor has no live
	// verdict, so admission depends on the cache.
	onlyWriterComment := func(t *testing.T) Input {
		doc := workItem(t)
		doc.Entity.Comments.Known = doc.Entity.Comments.Known[:1]
		in := input(t, doc, normentity.RoleTriage)
		delete(in.Roles.Live, actorID(t, writer))
		return in
	}

	t.Run("eligible entry within scope and age is reused", func(t *testing.T) {
		mem := actorcache.NewMemory(time.Minute)
		require.NoError(t, mem.Put(cacheKey(t, writer, normevent.ActorHuman, humanScope), actorcache.Entry{
			Role: normevent.RoleWrite, RoleVerified: true, LookupStart: time.Now().Add(-10 * time.Second),
		}))
		in := onlyWriterComment(t)
		in.Now = time.Now()
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope, in.Roles.CacheShared = mem, time.Minute, &humanScope, true
		v := build(t, in)
		assert.Equal(t, []string{"C_writer"}, commentIDs(&v.Document.Entity))
		assert.Equal(t, normentity.CoverageComplete, v.Document.Entity.Comments.Coverage.Status)
		assert.Equal(t, time.Minute, v.Evidence.CacheMaxAge)
		var w *ActorEvidence
		for i := range v.Evidence.Actors {
			if v.Evidence.Actors[i].Actor == actorID(t, writer) {
				w = &v.Evidence.Actors[i]
			}
		}
		require.NotNil(t, w)
		assert.Equal(t, SourceSharedCache, w.Source)
		assert.Equal(t, "collaborators", w.PermissionSource)
		assert.Equal(t, normentity.RoleWrite, w.Role)
	})

	t.Run("entry scoped to another policy revision is rejected", func(t *testing.T) {
		mem := actorcache.NewMemory(time.Minute)
		other := humanScope
		other.PolicyRevision = "p0"
		require.NoError(t, mem.Put(cacheKey(t, writer, normevent.ActorHuman, other), actorcache.Entry{
			Role: normevent.RoleWrite, RoleVerified: true, LookupStart: time.Now().Add(-time.Second),
		}))
		in := onlyWriterComment(t)
		in.Now = time.Now()
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope = mem, time.Minute, &humanScope
		v := build(t, in)
		assert.Empty(t, v.Document.Entity.Comments.Known)
		assert.Equal(t, normentity.CoverageUnknown, v.Document.Entity.Comments.Coverage.Status)
	})

	t.Run("invalidated entry is rejected", func(t *testing.T) {
		mem := actorcache.NewMemory(time.Minute)
		require.NoError(t, mem.Put(cacheKey(t, writer, normevent.ActorHuman, humanScope), actorcache.Entry{
			Role: normevent.RoleWrite, RoleVerified: true, LookupStart: time.Now().Add(-time.Second),
		}))
		mem.Invalidate(actorID(t, writer))
		in := onlyWriterComment(t)
		in.Now = time.Now()
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope = mem, time.Minute, &humanScope
		v := build(t, in)
		assert.Empty(t, v.Document.Entity.Comments.Known)
		assert.Equal(t, normentity.CoverageUnknown, v.Document.Entity.Comments.Coverage.Status)
	})

	t.Run("entry older than the platform age is rejected even if served", func(t *testing.T) {
		fc := &fakeCache{entries: map[actorcache.Key]actorcache.Entry{
			cacheKey(t, writer, normevent.ActorHuman, humanScope): {Role: normevent.RoleWrite, RoleVerified: true, LookupStart: checkTime.Add(-2 * time.Minute)},
		}}
		in := onlyWriterComment(t)
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope = fc, time.Minute, &humanScope
		v := build(t, in)
		assert.Equal(t, 1, fc.gets)
		assert.Empty(t, v.Document.Entity.Comments.Known)
	})

	t.Run("platform age above the hard cap is clamped", func(t *testing.T) {
		fc := &fakeCache{entries: map[actorcache.Key]actorcache.Entry{
			cacheKey(t, writer, normevent.ActorHuman, humanScope): {Role: normevent.RoleWrite, RoleVerified: true, LookupStart: checkTime.Add(-6 * time.Minute)},
		}}
		in := onlyWriterComment(t)
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope = fc, time.Hour, &humanScope
		v := build(t, in)
		assert.Equal(t, actorcache.HardMaxAge, v.Evidence.CacheMaxAge)
		assert.Empty(t, v.Document.Entity.Comments.Known)
	})

	t.Run("reuse is disabled without a platform age or scope", func(t *testing.T) {
		fc := &fakeCache{entries: map[actorcache.Key]actorcache.Entry{
			cacheKey(t, writer, normevent.ActorHuman, humanScope): {Role: normevent.RoleWrite, RoleVerified: true, LookupStart: checkTime.Add(-time.Second)},
		}}
		in := onlyWriterComment(t)
		in.Roles.Cache, in.Roles.HumanScope = fc, &humanScope
		v := build(t, in)
		assert.Zero(t, fc.gets, "no platform max age disables reuse")
		assert.Zero(t, v.Evidence.CacheMaxAge)
		assert.Empty(t, v.Document.Entity.Comments.Known)

		in = onlyWriterComment(t)
		in.Roles.Cache, in.Roles.MaxAge = fc, time.Minute
		build(t, in)
		assert.Zero(t, fc.gets, "no scope for the actor kind disables reuse")
	})

	t.Run("local bot verdict and malformed entries", func(t *testing.T) {
		fc := &fakeCache{entries: map[actorcache.Key]actorcache.Entry{
			cacheKey(t, registered, normevent.ActorBot, botScope): {Role: normevent.RoleNone, BotRole: "coder", RoleVerified: true, LookupStart: checkTime.Add(-time.Second)},
			cacheKey(t, unregBot, normevent.ActorBot, botScope):   {Role: normevent.RoleWrite, BotRole: "coder", RoleVerified: true, LookupStart: checkTime.Add(-time.Second)},
			cacheKey(t, writer, normevent.ActorHuman, humanScope): {Role: normevent.RoleWrite, BotRole: "coder", RoleVerified: true, LookupStart: checkTime.Add(-time.Second)},
		}}
		in := input(t, workItem(t), normentity.RoleTriage)
		in.Roles.Live = nil
		in.Roles.Cache, in.Roles.MaxAge, in.Roles.HumanScope, in.Roles.BotScope = fc, time.Minute, &humanScope, &botScope
		v := build(t, in)
		assert.Equal(t, []string{"C_bot"}, commentIDs(&v.Document.Entity))
		botRole, _ := v.Document.Entity.Comments.Known[0].Actor.BotRole.Value()
		assert.Equal(t, "coder", botRole)
		for _, a := range v.Evidence.Actors {
			if a.Actor == actorID(t, registered) {
				assert.Equal(t, SourceLocalCache, a.Source)
			}
		}
	})
}

func TestVerifiedBotNoMatchIsNull(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Author = normentity.Known(bot(unregBot))
	v := build(t, input(t, doc, normentity.RoleTriage))
	a := v.Document.Entity.Author.Ptr()
	require.NotNil(t, a)
	assert.True(t, a.BotRole.IsNull(), "a verified no-match is null, not the claimed role")
}

func TestSubjectWithoutEvidenceHasUnknownRole(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Author = normentity.Known(human(unknownAct))
	v := build(t, input(t, doc, normentity.RoleTriage))
	a := v.Document.Entity.Author.Ptr()
	require.NotNil(t, a)
	_, unknown := a.Role.UnknownReason()
	assert.True(t, unknown, "an unverified role claim is not passed through")
	_, unknown = a.RoleVerified.UnknownReason()
	assert.True(t, unknown)
	assert.True(t, a.BotRole.IsZero())

	doc.Entity.Author = normentity.Known(bot("B_none"))
	v = build(t, input(t, doc, normentity.RoleTriage))
	_, unknown = v.Document.Entity.Author.Ptr().BotRole.UnknownReason()
	assert.True(t, unknown)
}

func TestTeamActorCannotAuthorAnAction(t *testing.T) {
	doc := workItem(t)
	doc.Entity.Reactions.Known = []normentity.Reaction{{ID: "R1", Actor: normentity.Actor{
		ID: normentity.Known("TEAM_1"), Kind: normentity.Known(normentity.ActorTeam),
	}}}
	v := build(t, input(t, doc, normentity.RoleTriage))
	assert.Empty(t, v.Document.Entity.Reactions.Known)
	assert.Equal(t, Counts{Excluded: 1}, v.Evidence.Collections["reactions"].Counts)
}

func TestBuildRejectsInvalidInput(t *testing.T) {
	valid := input(t, workItem(t), normentity.RoleTriage)

	for _, role := range []normentity.Role{"", normentity.RoleRead, normentity.RoleNone, "owner"} {
		in := valid
		in.Policy.HumanMinRole = role
		_, err := Build(in)
		assert.ErrorIs(t, err, ErrInvalidPolicy, "role %q", role)
	}
	in := valid
	in.Policy.Identity = ""
	_, err := Build(in)
	assert.ErrorIs(t, err, ErrInvalidPolicy)

	in = valid
	in.Document = nil
	_, err = Build(in)
	assert.ErrorIs(t, err, ErrInvalidInput)

	in = valid
	in.Now = time.Time{}
	_, err = Build(in)
	assert.ErrorIs(t, err, ErrInvalidInput)

	bad := workItem(t)
	bad.Entity.ID = "fullsend:entity:v1:github:github.com:work_item:other"
	in = valid
	in.Document = bad
	_, err = Build(in)
	assert.ErrorIs(t, err, ErrInvalidInput)

	unmarshalable := workItem(t)
	unmarshalable.Entity.Title = normentity.Unknown[string]("bogus")
	in = valid
	in.Document = unmarshalable
	_, err = Build(in)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

// TestGateSeparation pins that the view is entity-view admission only: its
// evidence names that single gate and it consults the actor cache only for
// reads, so it cannot stand in for event, eventless-provenance, or
// model-bound content authorization.
func TestGateSeparation(t *testing.T) {
	fc := &fakeCache{}
	in := input(t, workItem(t), normentity.RoleTriage)
	in.Roles.Cache, in.Roles.MaxAge = fc, time.Minute
	in.Roles.HumanScope = &CacheScope{Tenant: "o", Repository: "R_1", Policy: "p", PolicyRevision: "1", PermissionSource: "c"}
	v := build(t, in)
	assert.Equal(t, "entity_view_admission", v.Evidence.Gate)
	assert.Equal(t, "policy-1", v.Evidence.PolicyIdentity)
	assert.Equal(t, revision, v.Evidence.SourceRevision)
	assert.Equal(t, checkTime, v.Evidence.CheckTime)
	assert.Equal(t, v.Document.Entity.ID, v.Evidence.Entity)
	assert.Empty(t, fc.entries, "the view never writes role evidence")
}

func TestEvidenceTotalsAndActorsAreBounded(t *testing.T) {
	v := build(t, input(t, workItem(t), normentity.RoleTriage))
	seen := map[actorcache.ActorID]bool{}
	for i, a := range v.Evidence.Actors {
		assert.False(t, seen[a.Actor], "each actor appears once")
		seen[a.Actor] = true
		if i > 0 {
			assert.LessOrEqual(t, string(v.Evidence.Actors[i-1].Actor), string(a.Actor))
		}
	}
	sum := Counts{}
	for _, c := range v.Evidence.Collections {
		sum.Admitted += c.Admitted
		sum.Excluded += c.Excluded
		sum.Unresolved += c.Unresolved
	}
	for _, f := range v.Evidence.Fields {
		sum.add(f.Outcome)
	}
	for k, c := range v.Evidence.Values {
		if k == "threads.comment_ids" {
			continue // memberships are tallied separately from totals
		}
		sum.Admitted += c.Admitted
		sum.Excluded += c.Excluded
		sum.Unresolved += c.Unresolved
	}
	assert.Equal(t, sum, v.Evidence.Totals)
}

// TestSpecExamplesProduceSerializableViews runs every valid specification
// example through Build: the view must remain a well-formed document, keep
// non-complete coverage non-complete, and keep unknown fields unknown.
func TestSpecExamplesProduceSerializableViews(t *testing.T) {
	const dir = "../../docs/normative/normalized-entity/v1/examples"
	paths, err := filepath.Glob(filepath.Join(dir, "valid-*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			raw, err := os.ReadFile(p)
			require.NoError(t, err)
			var doc normentity.Document
			require.NoError(t, json.Unmarshal(raw, &doc))
			v, err := Build(Input{
				Document: &doc,
				Policy:   Policy{Identity: "p", HumanMinRole: normentity.RoleWrite},
				Now:      checkTime,
			})
			require.NoError(t, err)
			out, err := json.Marshal(v.Document)
			require.NoError(t, err)
			var back normentity.Document
			require.NoError(t, json.Unmarshal(out, &back))
			require.NoError(t, back.Validate())

			// With no role evidence, nothing actor-attributed is admitted.
			assert.Zero(t, v.Evidence.Totals.Admitted)
			_, titleKnown := v.Document.Entity.Title.Value()
			assert.False(t, titleKnown)
			_, bodyKnown := v.Document.Entity.Body.Value()
			assert.False(t, bodyKnown)
			assert.Empty(t, v.Document.Entity.Comments.Known)
			if !doc.Entity.Comments.IsZero() && doc.Entity.Comments.Coverage.Status != normentity.CoverageComplete {
				assert.NotEqual(t, normentity.CoverageComplete, v.Document.Entity.Comments.Coverage.Status)
			}
		})
	}
}
