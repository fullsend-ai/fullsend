package e2eauth

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	testOwner = "fullsend-ai"
	testRepo  = "fullsend"
	testPR    = 42
	prKey     = "fullsend-ai/fullsend/42"
)

type harness struct {
	fc   *forge.FakeClient
	gate GitHubGate
}

func newHarness() *harness {
	fc := &forge.FakeClient{
		PullRequestAuthContexts:       make(map[string]forge.PullRequestAuthorizationContext),
		IssueLabelEvents:              make(map[string][]forge.IssueLabelEvent),
		CollaboratorPermissions:       make(map[string]string),
		CollaboratorPermissionDetails: make(map[string]forge.GitHubCollaboratorPermissionDetail),
		CollaboratorPermissionErrors:  make(map[string]error),
	}
	gate, err := GateFromForge(fc)
	if err != nil {
		panic(err)
	}
	return &harness{fc: fc, gate: gate}
}

func collabKey(user string) string {
	return testOwner + "/" + testRepo + "/" + user
}

func (h *harness) writePR(author string, labels []string, updatedAt string) {
	h.fc.PullRequestAuthContexts[prKey] = forge.PullRequestAuthorizationContext{
		AuthorLogin: author,
		UpdatedAt:   updatedAt,
		Labels:      labels,
	}
}

func (h *harness) setRole(user, spec string) {
	key := collabKey(user)
	delete(h.fc.CollaboratorPermissions, key)
	delete(h.fc.CollaboratorPermissionDetails, key)

	switch {
	case strings.HasPrefix(spec, "custom:"):
		parts := strings.SplitN(strings.TrimPrefix(spec, "custom:"), ":", 2)
		h.fc.CollaboratorPermissionDetails[key] = forge.GitHubCollaboratorPermissionDetail{
			RoleName:   parts[0],
			Permission: parts[1],
		}
	case strings.HasPrefix(spec, "mismatch:"):
		parts := strings.SplitN(strings.TrimPrefix(spec, "mismatch:"), ":", 2)
		h.fc.CollaboratorPermissionDetails[key] = forge.GitHubCollaboratorPermissionDetail{
			RoleName:   parts[0],
			Permission: parts[1],
		}
	case strings.HasPrefix(spec, "raw:"):
		if spec == "raw:{\"role_name\":\"\",\"permission\":\"none\"}" {
			h.fc.CollaboratorPermissionDetails[key] = forge.GitHubCollaboratorPermissionDetail{
				RoleName:   "",
				Permission: "none",
			}
		}
	default:
		h.fc.CollaboratorPermissions[key] = spec
	}
}

func (h *harness) setEvents(events []forge.IssueLabelEvent) {
	h.fc.IssueLabelEvents[prKey] = events
}

func (h *harness) runCase(t *testing.T, name string, in Input, wantAuth bool, wantReason string, wantRemoved bool) {
	t.Run(name, func(t *testing.T) {
		got := Check(context.Background(), h.gate, in)
		assert.Equal(t, wantAuth, got.Authorized, "authorized")
		assert.Equal(t, wantReason, got.Reason, "reason")
		assert.Equal(t, wantRemoved, got.LabelRemoved, "label_removed")
	})
}

func baseInput() Input {
	return Input{
		Owner:    testOwner,
		Repo:     testRepo,
		PRNumber: testPR,
		DryRun:   true,
	}
}
