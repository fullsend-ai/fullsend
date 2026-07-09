package harnessdispatch

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

func TestIsAuthorized_LabelAddedImplicitWrite(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "added",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitHub},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.True(t, IsAuthorized(ev))
}

func TestIsAuthorized_LabelAddedRequiresGitHub(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "added",
			},
		},
		Source: normevent.Source{System: normevent.SystemGitLab},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func TestIsAuthorized_LabelRemovedRequiresWriteRole(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{
			Kind: normevent.TransitionLabelChanged,
			Label: &normevent.LabelChange{
				Name:   "ready-for-ping",
				Action: "removed",
			},
		},
		Actor: normevent.Actor{
			ID:   "fullsend-ai-e2e[bot]",
			Kind: normevent.ActorBot,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))
}

func TestIsAuthorized_OpenedRequiresWriteRole(t *testing.T) {
	ev := &normevent.Event{
		Transition: normevent.Transition{Kind: normevent.TransitionOpened},
		Actor: normevent.Actor{
			ID:   "reporter",
			Kind: normevent.ActorHuman,
			Role: normevent.RoleNone,
		},
	}
	assert.False(t, IsAuthorized(ev))

	ev.Actor.Role = normevent.RoleWrite
	assert.True(t, IsAuthorized(ev))
}
