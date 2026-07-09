// Package normevent defines forge-neutral NormalizedEvent types for harness
// CEL dispatch (ADR 0061). Schema: docs/normative/normalized-event/v1/.
package normevent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Event is a forge-neutral routing input for fullsend dispatch.
type Event struct {
	Repo       string     `json:"repo"`
	Entity     Entity     `json:"entity"`
	Transition Transition `json:"transition"`
	Actor      Actor      `json:"actor"`
	State      State      `json:"state"`
	Source     Source     `json:"source"`
}

// EntityKind identifies work items vs change proposals.
type EntityKind string

const (
	EntityWorkItem       EntityKind = "work_item"
	EntityChangeProposal EntityKind = "change_proposal"
)

// Entity is the target work item or change proposal.
type Entity struct {
	Kind                 EntityKind            `json:"kind"`
	ID                   int                   `json:"id"`
	Key                  string                `json:"key,omitempty"`
	URL                  string                `json:"url"`
	LinkedChangeProposal *LinkedChangeProposal `json:"linked_change_proposal,omitempty"`
}

// LinkedChangeProposal links a work item to an associated change proposal.
type LinkedChangeProposal struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// TransitionKind is the semantic lifecycle transition.
type TransitionKind string

const (
	TransitionOpened          TransitionKind = "opened"
	TransitionReopened        TransitionKind = "reopened"
	TransitionEdited          TransitionKind = "edited"
	TransitionSynchronized    TransitionKind = "synchronized"
	TransitionUpdated         TransitionKind = "updated"
	TransitionClosed          TransitionKind = "closed"
	TransitionMerged          TransitionKind = "merged"
	TransitionMarkedReady     TransitionKind = "marked_ready"
	TransitionLabelChanged    TransitionKind = "label_changed"
	TransitionCommentAdded    TransitionKind = "comment_added"
	TransitionReviewSubmitted TransitionKind = "review_submitted"
)

// Transition describes what changed on the entity.
type Transition struct {
	Kind    TransitionKind `json:"kind"`
	Label   *LabelChange   `json:"label,omitempty"`
	Comment *Comment       `json:"comment,omitempty"`
	Review  *Review        `json:"review,omitempty"`
}

// LabelChange describes a label add/remove transition.
type LabelChange struct {
	Name   string `json:"name"`
	Action string `json:"action"` // added | removed
}

// Comment describes a comment_added transition.
type Comment struct {
	Command     string `json:"command,omitempty"`
	Body        string `json:"body"`
	Instruction string `json:"instruction,omitempty"`
}

// Review describes a review_submitted transition.
type Review struct {
	State      string `json:"state"`
	ReviewerID string `json:"reviewer_id"`
}

// ActorKind distinguishes humans from bots.
type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorBot   ActorKind = "bot"
)

// ActorRole is the effective repository permission level (ADR 0054).
type ActorRole string

const (
	RoleAdmin    ActorRole = "admin"
	RoleMaintain ActorRole = "maintain"
	RoleWrite    ActorRole = "write"
	RoleTriage   ActorRole = "triage"
	RoleRead     ActorRole = "read"
	RoleNone     ActorRole = "none"
	RoleExternal ActorRole = "external"
)

// Actor is the user or service account that caused the transition.
type Actor struct {
	ID             string    `json:"id"`
	Kind           ActorKind `json:"kind"`
	Role           ActorRole `json:"role"`
	IsEntityAuthor bool      `json:"is_entity_author"`
}

// State is a snapshot of entity metadata at event time.
type State struct {
	Labels         []string        `json:"labels"`
	ChangeProposal *ChangeProposal `json:"change_proposal,omitempty"`
}

// ChangeProposal carries branch/repo context for PR/MR workloads.
type ChangeProposal struct {
	ID       int    `json:"id"`
	HeadRepo string `json:"head_repo"`
	BaseRepo string `json:"base_repo"`
	HeadRef  string `json:"head_ref"`
	BaseRef  string `json:"base_ref"`
	HeadSHA  string `json:"head_sha,omitempty"`
	AuthorID string `json:"author_id"`
	IsFork   bool   `json:"is_fork"`
}

// SourceSystem identifies event provenance.
type SourceSystem string

const (
	SystemGitHub   SourceSystem = "github"
	SystemGitLab   SourceSystem = "gitlab"
	SystemJira     SourceSystem = "jira"
	SystemManual   SourceSystem = "manual"
	SystemSchedule SourceSystem = "schedule"
)

// Source describes the native forge event.
type Source struct {
	System    SourceSystem `json:"system"`
	RawType   string       `json:"raw_type"`
	RawAction string       `json:"raw_action,omitempty"`
}

// ParseJSON unmarshals and validates a NormalizedEvent from JSON.
func ParseJSON(data []byte) (*Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("parsing normalized event: %w", err)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return &e, nil
}

// Validate checks required fields and cross-field consistency.
func (e *Event) Validate() error {
	if strings.TrimSpace(e.Repo) == "" {
		return fmt.Errorf("normalized event: repo is required")
	}
	if strings.Contains(e.Repo, "..") {
		return fmt.Errorf("normalized event: repo path must not contain '..'")
	}
	if e.Entity.Kind == "" {
		return fmt.Errorf("normalized event: entity.kind is required")
	}
	if e.Entity.ID < 1 {
		return fmt.Errorf("normalized event: entity.id must be >= 1")
	}
	if strings.TrimSpace(e.Entity.URL) == "" {
		return fmt.Errorf("normalized event: entity.url is required")
	}
	if e.Source.System == "" {
		return fmt.Errorf("normalized event: source.system is required")
	}
	if strings.TrimSpace(e.Source.RawType) == "" {
		return fmt.Errorf("normalized event: source.raw_type is required")
	}
	if e.Transition.Kind == "" {
		return fmt.Errorf("normalized event: transition.kind is required")
	}
	if e.Actor.ID == "" {
		return fmt.Errorf("normalized event: actor.id is required")
	}
	if e.Actor.Role == "" {
		return fmt.Errorf("normalized event: actor.role is required")
	}
	if e.State.Labels == nil {
		return fmt.Errorf("normalized event: state.labels is required")
	}

	switch e.Transition.Kind {
	case TransitionLabelChanged:
		if e.Transition.Label == nil {
			return fmt.Errorf("normalized event: transition.label required for label_changed")
		}
	case TransitionCommentAdded:
		if e.Transition.Comment == nil {
			return fmt.Errorf("normalized event: transition.comment required for comment_added")
		}
	case TransitionReviewSubmitted:
		if e.Transition.Review == nil {
			return fmt.Errorf("normalized event: transition.review required for review_submitted")
		}
	}

	if e.Entity.Kind == EntityChangeProposal && e.Entity.LinkedChangeProposal != nil {
		return fmt.Errorf("normalized event: linked_change_proposal forbidden when entity.kind is change_proposal")
	}

	if e.State.ChangeProposal != nil && e.Entity.Kind == EntityWorkItem && e.Entity.LinkedChangeProposal == nil {
		return fmt.Errorf("normalized event: entity.linked_change_proposal required when state.change_proposal is set on work_item")
	}

	if e.Source.System == SystemJira && e.Entity.Key == "" {
		return fmt.Errorf("normalized event: entity.key required when source.system is jira")
	}

	return nil
}

// ToMap converts the event to a map for CEL evaluation.
func (e *Event) ToMap() (map[string]any, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// IsWriteAuthorized reports whether actor.role satisfies ADR 0054 dispatch gate.
func IsWriteAuthorized(role ActorRole) bool {
	switch role {
	case RoleAdmin, RoleMaintain, RoleWrite:
		return true
	default:
		return false
	}
}

// MapGitHubPermission maps GitHub collaborator API role_name to ActorRole.
func MapGitHubPermission(roleName string) ActorRole {
	switch strings.ToLower(strings.TrimSpace(roleName)) {
	case "admin":
		return RoleAdmin
	case "maintain":
		return RoleMaintain
	case "write":
		return RoleWrite
	case "triage":
		return RoleTriage
	case "read":
		return RoleRead
	default:
		return RoleNone
	}
}
