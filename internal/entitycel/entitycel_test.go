package entitycel

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

func complete() ne.Coverage { return ne.Coverage{Status: ne.CoverageComplete} }
func partial() ne.Coverage  { return ne.Coverage{Status: ne.CoveragePartial} }

func actor(login string, role ne.Role) ne.Actor {
	return ne.Actor{
		ID:           ne.Known("id-" + login),
		Login:        ne.Known(login),
		Kind:         ne.Known(ne.ActorHuman),
		Role:         ne.Known(role),
		RoleVerified: ne.Known(true),
	}
}

func comment(id, text string, a ne.Actor) ne.Comment {
	var c ne.Comment
	c.ID = id
	c.CreatedAt = "2026-01-02T03:04:05.000000000Z"
	c.Actor = a
	c.Text = ne.Known(text)
	c.Command = ne.Null[string]()
	if len(text) > 0 && text[0] == '/' {
		c.Command = ne.Known(text)
	}
	c.ThreadID = ne.Null[string]()
	return c
}

// workItem returns an open GitHub issue whose collections are complete.
func workItem(t *testing.T) *ne.Entity {
	t.Helper()
	id, err := ne.NewID("github", "github.com", "work_item", "100")
	require.NoError(t, err)
	return &ne.Entity{
		Kind:     ne.KindWorkItem,
		Source:   ne.Source{System: ne.SystemGitHub, Host: "github.com", Repository: "o/r", RepositoryID: "1"},
		ID:       id,
		SourceID: "100",
		Key:      "o/r#7",
		Number:   7,
		URL:      "https://github.com/o/r/issues/7",
		State:    ne.Known(ne.StateOpen),
		Title:    ne.Known("Fix it"),
		Author:   ne.Known(actor("alice", ne.RoleWrite)),
		Labels:   ne.List[string]{Known: []string{"bug"}, Coverage: complete()},
		Comments: ne.List[ne.Comment]{
			Known:    []ne.Comment{comment("c1", "/fs-fix", actor("bob", ne.RoleWrite))},
			Coverage: complete(),
		},
		Lifecycle: ne.Lifecycle{ClosedAt: ne.Null[ne.Timestamp](), ClosedBy: ne.Null[ne.Actor]()},
		Threads: ne.List[ne.Thread]{
			Known: []ne.Thread{{
				ID:         "t1",
				CreatedAt:  "2026-01-02T03:04:05.000000000Z",
				CommentIDs: ne.List[string]{Known: []string{"c1"}, Coverage: partial()},
			}},
			Coverage: complete(),
		},
	}
}

var commentEvent = map[string]any{"type": "comment.created", "comment": map[string]any{"body": "/fs-fix"}}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ne.Entity)
		expr    string
		event   map[string]any
		want    Result
		wantErr bool
	}{
		// Event context.
		{name: "present event, guarded access", expr: `event != null && event.type == "comment.created" && entity.state == "open"`, event: commentEvent, want: True},
		{name: "null event, guarded access", expr: `event != null && event.type == "comment.created" && entity.state == "open"`, want: False},
		{name: "present event, unguarded access", expr: `event.type == "comment.created"`, event: commentEvent, want: True},
		{name: "null event, unguarded access is an error", expr: `event.type == "comment.created"`, wantErr: true},
		{name: "null event is CEL null", expr: `event == null && entity.state == "open"`, want: True},
		{name: "null event, unguarded access absorbed by true entity branch", expr: `entity.state == "open" || event.type == "x"`, want: True},
		{name: "entity-only predicate, null event", expr: `entity.state == "open" && "bug" in entity.labels`, want: True},
		{name: "entity-only predicate, present event", expr: `entity.state == "open" && "bug" in entity.labels`, event: commentEvent, want: True},
		{name: "entity-only predicate, proven false", expr: `entity.state == "closed"`, want: False},
		{name: "source system selector", expr: `entity.source.system == "github" && entity.number == 7`, want: True},

		// Unknown fields.
		{name: "unknown field comparison", mutate: unknownTitle, expr: `entity.title == "Fix it"`, want: Unknown},
		{name: "unknown field negation", mutate: unknownTitle, expr: `entity.title != "Fix it"`, want: Unknown},
		{name: "unknown field under negated predicate", mutate: unknownTitle, expr: `!(entity.title == "Fix it")`, want: Unknown},
		{name: "unknown field absorbed by false conjunct", mutate: unknownTitle, expr: `entity.state == "closed" && entity.title == "x"`, want: False},
		{name: "unknown field absorbed by true disjunct", mutate: unknownTitle, expr: `entity.state == "open" || entity.title == "x"`, want: True},
		{name: "presence of unknown applicable field is not false", mutate: unknownTitle, expr: `has(entity.title)`, want: Unknown},
		{name: "unknown field in string function", mutate: unknownTitle, expr: `entity.title.startsWith("Fix")`, want: Unknown},
		{name: "nested unknown field", mutate: func(e *ne.Entity) {
			a := actor("alice", ne.RoleWrite)
			a.Role = ne.Unknown[ne.Role](ne.ReasonFailed)
			e.Author = ne.Known(a)
		}, expr: `entity.author.role == "admin"`, want: Unknown},
		{name: "unknown struct field", mutate: func(e *ne.Entity) {
			e.Author = ne.Unknown[ne.Actor](ne.ReasonNotLoaded)
		}, expr: `entity.author.login == "alice"`, want: Unknown},
		{name: "list literal holding unknown", mutate: unknownTitle, expr: `[entity.title] == ["Fix it"]`, want: Unknown},
		{name: "unknown member of list literal", mutate: unknownTitle, expr: `"a" in [entity.title]`, want: Unknown},
		// cel-go's built-in map and list equality would report these
		// nested-unknown comparisons as true.
		{name: "nested unknown in map equality", mutate: unknownAuthorRole, expr: `entity.author == ` + aliceMap, want: Unknown},
		{name: "nested unknown in map inequality", mutate: unknownAuthorRole, expr: `entity.author != ` + aliceMap, want: Unknown},
		{name: "nested unknown in list equality", mutate: unknownAuthorRole, expr: `[entity.author] == [` + aliceMap + `]`, want: Unknown},
		{name: "nested unknown in map of different size", mutate: unknownAuthorRole, expr: `entity.author == {"login": "alice"}`, want: False},
		{name: "nested unknown in map with different keys", mutate: unknownAuthorRole, expr: `entity.author == {"id": "id-alice", "login": "alice", "kind": "human", "role": "write", "bot": true}`, want: False},
		{name: "nested unknown in map with different known value", mutate: unknownAuthorRole, expr: `entity.author == {"id": "id-alice", "login": "bob", "kind": "human", "role": "write", "role_verified": true}`, want: False},
		{name: "nested unknown in list of different size", mutate: unknownAuthorRole, expr: `[entity.author] == []`, want: False},
		{name: "nested unknown membership", mutate: unknownAuthorRole, expr: aliceMap + ` in [entity.author]`, want: Unknown},
		{name: "nested unknown does not hide known match", mutate: unknownAuthorRole, expr: `{"a": 1} in [entity.author, {"a": 1}]`, want: True},
		{name: "nested unknown, no possible match", mutate: unknownAuthorRole, expr: `{"a": 1} in [entity.author]`, want: False},
		{name: "map key membership", expr: `"login" in entity.author`, want: True},
		{name: "unknown element membership", mutate: unknownTitle, expr: `entity.title in ["a", "b"]`, want: Unknown},
		{name: "known nested map membership", expr: `{"a": 1} in [{"a": 1}]`, want: True},
		{name: "null field", expr: `entity.lifecycle.closed_at == null`, want: True},
		{name: "absent field", expr: `has(entity.change_proposal)`, want: False},
		{name: "unresolved change proposal", mutate: func(e *ne.Entity) {
			e.Kind = ne.KindChangeProposal
			e.ChangeProposal = ne.Unknown[ne.ChangeProposal](ne.ReasonNotLoaded)
		}, expr: `entity.change_proposal.draft == false`, want: Unknown},

		// Collections.
		{name: "complete collection, match", expr: `entity.comments.exists(c, c.command == "/fs-fix")`, want: True},
		{name: "complete collection, no match", expr: `entity.comments.exists(c, c.command == "/fs-retry")`, want: False},
		{name: "partial collection, known match", mutate: partialComments, expr: `entity.comments.exists(c, c.command == "/fs-fix")`, want: True},
		{name: "partial collection, no known match", mutate: partialComments, expr: `entity.comments.exists(c, c.command == "/fs-retry")`, want: Unknown},
		{name: "partial collection cannot prove absence", mutate: partialComments, expr: `!entity.comments.exists(c, c.command == "/fs-retry")`, want: Unknown},
		{name: "partial collection, known counterexample", mutate: partialComments, expr: `entity.comments.all(c, c.actor.role == "admin")`, want: False},
		{name: "partial collection, all known members satisfy", mutate: partialComments, expr: `entity.comments.all(c, c.actor.role == "write")`, want: Unknown},
		{name: "empty partial collection, member-independent exists", mutate: emptyPartialComments, expr: `entity.comments.exists(c, true)`, want: Unknown},
		{name: "empty partial collection, member-independent all", mutate: emptyPartialComments, expr: `entity.comments.all(c, false)`, want: Unknown},
		{name: "empty partial collection, size", mutate: emptyPartialComments, expr: `size(entity.comments) == 0`, want: Unknown},
		{name: "empty partial collection, equals empty", mutate: emptyPartialComments, expr: `entity.comments == []`, want: Unknown},
		{name: "empty partial collection, empty equals", mutate: emptyPartialComments, expr: `[] == entity.comments`, want: Unknown},
		{name: "empty partial collection, not equals empty", mutate: emptyPartialComments, expr: `entity.comments != []`, want: Unknown},
		{name: "partial collection longer than literal", mutate: partialComments, expr: `entity.comments == []`, want: False},
		{name: "empty complete collection, size", mutate: emptyCompleteComments, expr: `size(entity.comments) == 0`, want: True},
		{name: "empty complete collection, equals empty", mutate: emptyCompleteComments, expr: `[] == entity.comments`, want: True},
		{name: "known member with unknown field", mutate: func(e *ne.Entity) {
			c := comment("c2", "hello", actor("carol", ne.RoleWrite))
			c.Text = ne.Unknown[string](ne.ReasonPartial)
			e.Comments.Known = []ne.Comment{c}
		}, expr: `entity.comments.exists(c, c.text == "/fs-fix")`, want: Unknown},
		{name: "partial labels, known member", mutate: partialLabels, expr: `"bug" in entity.labels`, want: True},
		{name: "partial labels, other member", mutate: partialLabels, expr: `"wontfix" in entity.labels`, want: Unknown},
		{name: "partial labels, negated membership", mutate: partialLabels, expr: `!("wontfix" in entity.labels)`, want: Unknown},
		{name: "complete labels, negated membership", expr: `!("wontfix" in entity.labels)`, want: True},
		{name: "partial labels, index", mutate: partialLabels, expr: `entity.labels[0] == "bug"`, want: Unknown},
		{name: "partial labels, exists_one", mutate: partialLabels, expr: `entity.labels.exists_one(l, l == "bug")`, want: Unknown},
		{name: "complete labels, exists_one", expr: `entity.labels.exists_one(l, l == "bug")`, want: True},
		{name: "partial labels, filter size", mutate: partialLabels, expr: `entity.labels.filter(l, l.startsWith("b")).size() == 1`, want: Unknown},
		{name: "complete labels, filter size", expr: `entity.labels.filter(l, l.startsWith("b")).size() == 1`, want: True},
		{name: "partial labels, map", mutate: partialLabels, expr: `entity.labels.map(l, l + "!") == ["bug!"]`, want: Unknown},
		{name: "complete labels, map", expr: `entity.labels.map(l, l + "!") == ["bug!"]`, want: True},
		{name: "partial labels, concat membership", mutate: partialLabels, expr: `"bug" in (["x"] + entity.labels)`, want: True},
		{name: "partial labels, concat size", mutate: partialLabels, expr: `size(["x"] + entity.labels) == 2`, want: Unknown},
		{name: "partial labels, concat on the left", mutate: partialLabels, expr: `entity.labels + ["x"] == ["bug", "x"]`, want: Unknown},
		{name: "partial labels, nested in a list", mutate: partialLabels, expr: `[entity.labels] == [["bug"]]`, want: Unknown},
		{name: "partial labels, compared with non-list", mutate: partialLabels, expr: `entity.labels == "bug"`, want: False},
		{name: "complete list concat", expr: `["x"] + entity.labels == ["x", "bug"]`, want: True},
		{name: "nested partial collection, known match", expr: `entity.threads.exists(t, t.comment_ids.exists(id, id == "c1"))`, want: True},
		{name: "nested partial collection, emptiness", expr: `entity.threads.exists(t, size(t.comment_ids) == 0)`, want: Unknown},
		{name: "map range is not partial", expr: `{"a": 1}.exists(k, k == "a")`, want: True},

		// Errors.
		{name: "empty predicate", expr: "  ", wantErr: true},
		{name: "non-bool predicate", expr: `entity.title`, wantErr: true},
		{name: "compile error", expr: `entity.title ==`, wantErr: true},
		{name: "invalid coverage", mutate: func(e *ne.Entity) { e.Labels.Coverage.Status = "bogus" }, expr: `true`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := workItem(t)
			if tt.mutate != nil {
				tt.mutate(e)
			}
			got, err := Evaluate(context.Background(), tt.expr, e, tt.event)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, Unknown, got, "an error must never read as a definite result")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEvaluateNonCompleteCoverageStaysUnknown(t *testing.T) {
	for _, status := range []ne.CoverageStatus{
		"", ne.CoveragePartial, ne.CoverageNotLoaded, ne.CoverageUnknown, ne.CoverageUnsupported, ne.CoverageFailed,
	} {
		t.Run(string(status)+"_", func(t *testing.T) {
			e := workItem(t)
			e.Labels = ne.List[string]{Known: []string{}, Coverage: ne.Coverage{Status: status}}
			for _, expr := range []string{
				`entity.labels.exists(l, l == "bug")`,
				`!entity.labels.exists(l, l == "bug")`,
				`size(entity.labels) == 0`,
				`entity.labels == []`,
				`"bug" in entity.labels`,
			} {
				got, err := Evaluate(context.Background(), expr, e, nil)
				require.NoError(t, err, expr)
				assert.Equal(t, Unknown, got, expr)
			}
		})
	}
}

func TestEvaluateUnknownReasonsStayUnknown(t *testing.T) {
	for _, reason := range []ne.UnknownReason{
		ne.ReasonNotLoaded, ne.ReasonPartial, ne.ReasonUnsupported, ne.ReasonFailed, ne.ReasonConsistencyUnknown,
	} {
		t.Run(string(reason), func(t *testing.T) {
			e := workItem(t)
			e.State = ne.Unknown[ne.State](reason)
			for _, expr := range []string{`entity.state == "open"`, `entity.state != "open"`} {
				got, err := Evaluate(context.Background(), expr, e, nil)
				require.NoError(t, err, expr)
				assert.Equal(t, Unknown, got, expr)
			}
		})
	}
}

func TestEvaluateExtensions(t *testing.T) {
	e := workItem(t)
	s := "high"
	e.Extensions = &ne.Extensions{
		CustomFields: map[string]ne.ExtensionEntry{
			"priority": {SourceKey: "customfield_1", Value: ne.Known(ne.ExtensionValue{Atom: &ne.ExtensionAtom{String: &s}})},
			"teams": {SourceKey: "customfield_2", Value: ne.Known(ne.ExtensionValue{List: &ne.List[ne.ExtensionAtom]{
				Known:    []ne.ExtensionAtom{{String: &s}},
				Coverage: partial(),
			}})},
		},
	}
	tests := map[string]Result{
		`entity.extensions.custom_fields.priority.value == "high"`:           True,
		`entity.extensions.custom_fields.teams.value.exists(t, t == "high")`: True,
		`size(entity.extensions.custom_fields.teams.value) == 1`:             Unknown,
		`size(entity.extensions.properties) == 0`:                            True,
	}
	for expr, want := range tests {
		got, err := Evaluate(context.Background(), expr, e, nil)
		require.NoError(t, err, expr)
		assert.Equal(t, want, got, expr)
	}

	e.Extensions.CustomFields["bad"] = ne.ExtensionEntry{SourceKey: "x", Value: ne.Known(ne.ExtensionValue{})}
	_, err := Evaluate(context.Background(), `true`, e, nil)
	require.ErrorIs(t, err, ErrMalformedEntity)

	e.Extensions.CustomFields["bad"] = ne.ExtensionEntry{SourceKey: "x", Value: ne.Known(ne.ExtensionValue{Atom: &ne.ExtensionAtom{}})}
	_, err = Evaluate(context.Background(), `true`, e, nil)
	require.Error(t, err)
}

func TestBind(t *testing.T) {
	e := workItem(t)
	e.Title = ne.Unknown[string](ne.ReasonNotLoaded)
	e.Source.Project = ""
	e.Labels.Coverage = partial()
	bound, err := Bind(e)
	require.NoError(t, err)

	assert.Equal(t, "work_item", bound["kind"])
	assert.Equal(t, int64(7), bound["number"])
	assert.Equal(t, map[string]any{"system": "github", "host": "github.com", "repository": "o/r", "repository_id": "1"},
		bound["source"], "omitempty members are omitted")
	assert.NotContains(t, bound, "change_proposal", "absent fields are omitted")
	assert.NotContains(t, bound, "UnresolvedChangeProposal", "host-only members are not bound")

	unk, ok := bound["title"].(*types.Unknown)
	require.True(t, ok, "unknown field binds to CEL unknown, got %T", bound["title"])
	assert.Contains(t, unk.String(), "entity.title")

	lifecycle := bound["lifecycle"].(map[string]any)
	assert.Equal(t, types.NullValue, lifecycle["closed_at"])

	_, ok = bound["labels"].(*partialList)
	assert.True(t, ok, "partial collection binds to a partial list")

	comments, ok := bound["comments"].([]any)
	require.True(t, ok, "complete collection binds to an ordinary list")
	c := comments[0].(map[string]any)
	assert.Equal(t, "c1", c["id"], "embedded action members are flattened")
	assert.Equal(t, "/fs-fix", c["text"])
	assert.Equal(t, types.NullValue, c["thread_id"])

	_, err = Bind(nil)
	require.Error(t, err)
	_, err = Evaluate(context.Background(), `true`, nil, nil)
	require.ErrorIs(t, err, ErrMalformedEntity)
}

func TestValidate(t *testing.T) {
	require.NoError(t, Validate(`event != null && event.type == "x"`))
	require.NoError(t, Validate(`entity.comments.exists(c, c.command == "/fs-fix")`))
	require.NoError(t, Validate(`entity.labels.map(l, l.size() > 0, l).size() > 0`))
	require.Error(t, Validate(""), "an entity-source harness needs a predicate")
	require.Error(t, Validate(`entity.title`))
	require.Error(t, Validate(`entity.`))
	require.Error(t, Validate(`@entitycel_known(entity.labels).size() > 0`), "internal functions are not callable")
}

// chainedMaps returns a predicate with n chained map comprehensions, whose
// unbounded expansion would grow as 3^n.
func chainedMaps(n int) string {
	return "[1]" + strings.Repeat(".map(x, x)", n) + ".size() == 1"
}

// nestedMaps returns a predicate with n comprehensions nested in the
// argument position, whose unbounded expansion would grow as 2^n.
func nestedMaps(n int) string {
	return strings.Repeat("[1].map(x, ", n) + "x" + strings.Repeat(")", n) + ".size() == 1"
}

func TestValidateBoundsMacroExpansion(t *testing.T) {
	require.NoError(t, Validate(chainedMaps(5)), "modest chaining stays valid")
	require.NoError(t, Validate(nestedMaps(5)), "modest nesting stays valid")

	for name, expr := range map[string]string{
		"chained": chainedMaps(60),
		"nested":  nestedMaps(60),
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- Validate(expr) }()
			select {
			case err := <-done:
				require.Error(t, err)
				assert.Contains(t, err.Error(), "expression nodes")
			case <-time.After(10 * time.Second):
				t.Fatal("compiling a deeply chained predicate was not bounded")
			}
		})
	}
}

// intList returns a CEL list literal of n integers.
func intList(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprint(i)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestEvaluateBoundsTime(t *testing.T) {
	l := intList(100)
	expr := fmt.Sprintf(`%[1]s.all(a, %[1]s.all(b, %[1]s.all(c, a + b + c >= 0)))`, l)
	require.NoError(t, Validate(expr), "the predicate itself is well formed")

	old := evalTimeout
	evalTimeout = 10 * time.Millisecond
	t.Cleanup(func() { evalTimeout = old })

	got, err := Evaluate(context.Background(), expr, workItem(t), nil)
	require.Error(t, err, "cubic work exceeds the evaluation budget")
	assert.Equal(t, Unknown, got, "a timeout error is never a definite answer")
}

func TestEvaluateStopsOnCancelledContext(t *testing.T) {
	l := intList(50)
	expr := fmt.Sprintf(`%[1]s.all(a, %[1]s.all(b, a + b >= 0))`, l)

	got, err := Evaluate(context.Background(), expr, workItem(t), nil)
	require.NoError(t, err, "within the budget it completes")
	assert.Equal(t, True, got)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = Evaluate(ctx, expr, workItem(t), nil)
	require.Error(t, err, "a cancelled context interrupts evaluation")
	assert.Equal(t, Unknown, got)
}

func TestEvaluateCancelledContextSkipsSetup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A simple predicate has no comprehension to interrupt, and a malformed
	// entity would otherwise surface ErrMalformedEntity from binding: the
	// context error proves nothing past the entry check ran.
	got, err := Evaluate(ctx, `true`, nil, nil)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrMalformedEntity)
	assert.Equal(t, Unknown, got)
}

func TestPartialListValue(t *testing.T) {
	p := newPartialList([]any{"a"}, unknownAt([]any{"labels"}))
	assert.Equal(t, types.ListType, p.Type())
	assert.Equal(t, types.ListType, p.ConvertToType(types.TypeType))
	assert.Equal(t, p, p.ConvertToType(types.ListType))
	assert.True(t, types.IsUnknown(p.ConvertToType(types.StringType)))
	_, err := p.ConvertToNative(nil)
	require.Error(t, err)
	assert.Len(t, p.Value(), 1)
	assert.Equal(t, types.True, p.Contains(types.String("a")))
	assert.True(t, types.IsUnknown(p.Contains(types.String("b"))))
	assert.True(t, types.IsUnknown(p.Equal(types.NewDynamicList(types.DefaultTypeAdapter, []string{"a"}))))
	assert.True(t, types.IsError(p.Get(types.String("x"))))
	assert.True(t, types.IsUnknown(p.Size()))
	assert.True(t, types.IsUnknown(p.Get(types.Int(0))))
	assert.True(t, types.IsUnknown(concat(p, p).(*partialList).remainder))
	assert.Len(t, concat(p, p).(*partialList).known, 2)
	assert.True(t, types.IsError(concat(p, types.String("x"))))
	assert.True(t, types.IsError(concat(types.String("x"), p)))

	it := p.Iterator()
	assert.Equal(t, types.IteratorType, it.Type())
	assert.Equal(t, types.String("a"), it.Next())
	assert.True(t, types.IsUnknown(it.Next()))
	assert.Equal(t, types.False, it.HasNext())
	assert.Nil(t, it.Next())
	assert.Nil(t, it.Value())
	assert.True(t, types.IsError(it.Equal(types.True)))
	assert.True(t, types.IsError(it.ConvertToType(types.ListType)))
	_, err = it.ConvertToNative(nil)
	require.Error(t, err)
}

func TestResultString(t *testing.T) {
	assert.Equal(t, "true", True.String())
	assert.Equal(t, "false", False.String())
	assert.Equal(t, "unknown", Unknown.String())
	assert.Equal(t, Unknown, Result(0), "the zero result is not definite")
}

func unknownTitle(e *ne.Entity)    { e.Title = ne.Unknown[string](ne.ReasonNotLoaded) }
func partialComments(e *ne.Entity) { e.Comments.Coverage = partial() }
func emptyPartialComments(e *ne.Entity) {
	e.Comments = ne.List[ne.Comment]{Known: []ne.Comment{}, Coverage: partial()}
}
func emptyCompleteComments(e *ne.Entity) {
	e.Comments = ne.List[ne.Comment]{Known: []ne.Comment{}, Coverage: complete()}
}
func partialLabels(e *ne.Entity) { e.Labels.Coverage = partial() }

const aliceMap = `{"id": "id-alice", "login": "alice", "kind": "human", "role": "write", "role_verified": true}`

func unknownAuthorRole(e *ne.Entity) {
	a := actor("alice", ne.RoleWrite)
	a.Role = ne.Unknown[ne.Role](ne.ReasonFailed)
	e.Author = ne.Known(a)
}
