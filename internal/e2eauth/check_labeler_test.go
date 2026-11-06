package e2eauth

import (
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
)

func TestLatestOkToTestLabeler(t *testing.T) {
	t.Run("max timestamp wins", func(t *testing.T) {
		events := []forge.IssueLabelEvent{
			{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T09:00:00Z", ActorLogin: "alice"},
			{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
		}
		assert.Equal(t, "triager", latestOkToTestLabeler(events))
	})

	t.Run("same timestamp different labelers is ambiguous", func(t *testing.T) {
		events := []forge.IssueLabelEvent{
			{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
			{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
		}
		assert.Empty(t, latestOkToTestLabeler(events))
	})

	t.Run("same timestamp empty actor before real labeler is ambiguous", func(t *testing.T) {
		ts := "2026-06-01T11:00:00Z"
		events := []forge.IssueLabelEvent{
			{LabelName: OkToTestLabel, CreatedAt: ts, ActorLogin: ""},
			{LabelName: OkToTestLabel, CreatedAt: ts, ActorLogin: "triager"},
		}
		assert.Empty(t, latestOkToTestLabeler(events))
	})

	t.Run("same timestamp real labeler before empty actor is ambiguous", func(t *testing.T) {
		ts := "2026-06-01T11:00:00Z"
		events := []forge.IssueLabelEvent{
			{LabelName: OkToTestLabel, CreatedAt: ts, ActorLogin: "triager"},
			{LabelName: OkToTestLabel, CreatedAt: ts, ActorLogin: ""},
		}
		assert.Empty(t, latestOkToTestLabeler(events))
	})

	t.Run("ignores other labels", func(t *testing.T) {
		events := []forge.IssueLabelEvent{
			{LabelName: "bug", CreatedAt: "2026-06-01T12:00:00Z", ActorLogin: "other"},
			{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
		}
		assert.Equal(t, "labeler", latestOkToTestLabeler(events))
	})
}

func TestCheck_SameTimestampOkToTestDoesNotRemoveOnAmbiguousLatest(t *testing.T) {
	h := newHarness()
	in := baseInput()
	in.DryRun = true
	in.EventAction = "labeled"
	in.LabelActorLogin = "triager"
	in.PRAuthorLogin = "contributor"
	h.setRole("contributor", "none")
	h.setRole("triager", "triage")
	h.writePR("contributor", []string{OkToTestLabel}, "")
	h.setEvents([]forge.IssueLabelEvent{
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "triager"},
		{LabelName: OkToTestLabel, CreatedAt: "2026-06-01T11:00:00Z", ActorLogin: "labeler"},
	})
	h.runCase(t, "ambiguous latest skips removal", in, false, "untrusted_labeler", false)
}
