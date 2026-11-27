package entityacquire

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ne "github.com/fullsend-ai/fullsend/internal/normentity"
)

func TestDerivePlan(t *testing.T) {
	tests := []struct {
		name         string
		expr         string
		facts        []Need
		collections  map[Need]Proof
		conservative bool
	}{
		{name: "state only", expr: `entity.state == "open"`, facts: []Need{NeedState}},
		{name: "identity needs nothing", expr: `entity.id != "" && entity.number > 0 && entity.source.system == "github"`},
		{name: "unknown member needs nothing", expr: `entity.mystery == 1`},
		{name: "presence test", expr: `has(entity.title)`, facts: []Need{NeedTitle}},
		{
			name:        "exists is a witness",
			expr:        `entity.labels.exists(l, l == "bug")`,
			collections: map[Need]Proof{NeedLabels: ProofWitness},
		},
		{
			name:        "in is a witness",
			expr:        `"bug" in entity.labels`,
			collections: map[Need]Proof{NeedLabels: ProofWitness},
		},
		{
			name:        "constant index is a selection",
			expr:        `entity["labels"].exists(l, l == "bug")`,
			collections: map[Need]Proof{NeedLabels: ProofWitness},
		},
		{
			name:        "negation needs complete",
			expr:        `!entity.labels.exists(l, l == "bug")`,
			collections: map[Need]Proof{NeedLabels: ProofComplete},
		},
		{
			name:        "size needs complete",
			expr:        `size(entity.comments) == 0`,
			collections: map[Need]Proof{NeedComments: ProofComplete},
		},
		{
			name:        "all needs complete",
			expr:        `entity.comments.all(c, c.text != "")`,
			collections: map[Need]Proof{NeedComments: ProofComplete},
		},
		{
			name:        "map needs complete",
			expr:        `entity.comments.map(c, c.text).exists(t, t == "x")`,
			collections: map[Need]Proof{NeedComments: ProofComplete},
		},
		{
			name:        "positional index needs complete",
			expr:        `entity.comments[0].text == "x"`,
			collections: map[Need]Proof{NeedComments: ProofComplete},
		},
		{
			name:        "conditional test needs complete",
			expr:        `entity.labels.exists(l, l == "x") ? true : false`,
			collections: map[Need]Proof{NeedLabels: ProofComplete},
		},
		{
			name:        "conditional branches stay positive",
			expr:        `entity.state == "open" ? entity.labels.exists(l, l == "x") : false`,
			facts:       []Need{NeedState},
			collections: map[Need]Proof{NeedLabels: ProofWitness},
		},
		{
			name:        "strongest use wins",
			expr:        `entity.labels.exists(l, l == "a") && size(entity.labels) < 3`,
			collections: map[Need]Proof{NeedLabels: ProofComplete},
		},
		{
			name:        "nested exists body stays positive",
			expr:        `entity.comments.exists(c, entity.labels.exists(l, l == c.text))`,
			collections: map[Need]Proof{NeedComments: ProofWitness, NeedLabels: ProofWitness},
		},
		{
			name:        "only the referenced categories",
			expr:        `entity.state == "open" && (entity.labels.exists(l, l == "x") || entity.comments.exists(c, c.command == "/go"))`,
			facts:       []Need{NeedState},
			collections: map[Need]Proof{NeedLabels: ProofWitness, NeedComments: ProofWitness},
		},
		{name: "change proposal scalar", expr: `entity.change_proposal.draft == false`, facts: []Need{NeedChangeProposal}},
		{
			name:        "change proposal collection",
			expr:        `entity.change_proposal.changed_files.exists(f, f.path == "go.mod")`,
			facts:       []Need{NeedChangeProposal},
			collections: map[Need]Proof{NeedChangedFiles: ProofWitness},
		},
		{
			name:        "change proposal commit member",
			expr:        `entity.change_proposal.commits[0] == "abc"`,
			facts:       []Need{NeedChangeProposal},
			collections: map[Need]Proof{NeedCommits: ProofComplete},
		},
		{
			name:        "change proposal as a value",
			expr:        `entity.change_proposal != null`,
			facts:       []Need{NeedChangeProposal},
			collections: map[Need]Proof{NeedChangedFiles: ProofComplete, NeedCommits: ProofComplete},
		},
		{name: "literals", expr: `[entity.title].exists(t, t == "x") && {"a": entity.body}.a != null`, facts: []Need{NeedTitle, NeedBody}},
		{name: "member call target", expr: `entity.title.startsWith("x")`, facts: []Need{NeedTitle}},
		{name: "event only", expr: `event != null && event.kind == "x"`},
		{name: "whole entity is conservative", expr: `entity.exists(k, k == "labels")`, conservative: true},
		{name: "dynamic key is conservative", expr: `entity[entity.title] == 1`, conservative: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := DerivePlan(tt.expr)
			require.NoError(t, err)
			assert.Equal(t, tt.conservative, p.Conservative)
			if tt.conservative {
				assert.Equal(t, factNeeds, p.Facts)
				assert.Len(t, p.Collections, len(collectionNeeds))
				for _, proof := range p.Collections {
					assert.Equal(t, ProofComplete, proof)
				}
				return
			}
			assert.Equal(t, tt.facts, p.Facts)
			want := tt.collections
			if want == nil {
				want = map[Need]Proof{}
			}
			assert.Equal(t, want, p.Collections)
		})
	}
}

func TestDerivePlanRejectsInvalidPredicates(t *testing.T) {
	for _, expr := range []string{"", `entity.state ==`, `entity.state`} {
		_, err := DerivePlan(expr)
		assert.Error(t, err, expr)
	}
}

func TestCollectionOrder(t *testing.T) {
	p, err := DerivePlan(`entity.change_proposal.commits.exists(c, c == "a") || entity.comments.exists(c, c.text == "a") || "x" in entity.labels`)
	require.NoError(t, err)
	assert.Equal(t, []Need{NeedLabels, NeedComments, NeedCommits}, p.CollectionOrder())
}

func TestNeedAppliesTo(t *testing.T) {
	assert.True(t, NeedLabels.AppliesTo(ne.KindWorkItem))
	assert.True(t, NeedWorkItem.AppliesTo(ne.KindWorkItem))
	assert.False(t, NeedWorkItem.AppliesTo(ne.KindChangeProposal))
	assert.False(t, NeedReviews.AppliesTo(ne.KindWorkItem))
	assert.True(t, NeedReviews.AppliesTo(ne.KindChangeProposal))
	assert.True(t, NeedConversation.AppliesTo(ne.KindConversation))
	assert.False(t, NeedCommits.AppliesTo(ne.KindConversation))
	assert.True(t, NeedComments.IsCollection())
	assert.False(t, NeedState.IsCollection())
}

func TestProofString(t *testing.T) {
	assert.Equal(t, "witness", ProofWitness.String())
	assert.Equal(t, "complete", ProofComplete.String())
	assert.Equal(t, "none", Proof(0).String())
}
