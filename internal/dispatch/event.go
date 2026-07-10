package dispatch

// NormalizedEvent is the forge-neutral routing input for dispatch and
// harness CEL trigger evaluation. See docs/normative/normalized-event/v1/.
type NormalizedEvent struct {
	Repo       string     `json:"repo"`
	Entity     Entity     `json:"entity"`
	Transition Transition `json:"transition"`
	Actor      Actor      `json:"actor"`
	State      State      `json:"state"`
	Source     Source     `json:"source"`
}

// Entity identifies the work item or change proposal the event acts on.
type Entity struct {
	Kind                 string                `json:"kind"`
	ID                   int                   `json:"id"`
	URL                  string                `json:"url"`
	Key                  string                `json:"key,omitempty"`
	LinkedChangeProposal *LinkedChangeProposal `json:"linked_change_proposal,omitempty"`
}

// LinkedChangeProposal links a work_item entity to its associated change proposal.
type LinkedChangeProposal struct {
	ID  int    `json:"id"`
	URL string `json:"url"`
}

// Transition describes the lifecycle event that occurred.
type Transition struct {
	Kind    string             `json:"kind"`
	Label   *TransitionLabel   `json:"label,omitempty"`
	Comment *TransitionComment `json:"comment,omitempty"`
	Review  *TransitionReview  `json:"review,omitempty"`
}

// TransitionLabel carries label change details (kind == "label_changed").
type TransitionLabel struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

// TransitionComment carries comment details (kind == "comment_added").
type TransitionComment struct {
	Command     string `json:"command,omitempty"`
	Body        string `json:"body"`
	Instruction string `json:"instruction,omitempty"`
}

// TransitionReview carries review details (kind == "review_submitted").
type TransitionReview struct {
	State      string `json:"state"`
	ReviewerID string `json:"reviewer_id"`
}

// Actor identifies who triggered the event.
type Actor struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Role           string `json:"role"`
	IsEntityAuthor bool   `json:"is_entity_author"`
}

// State captures the entity's state at event time.
type State struct {
	Labels         []string             `json:"labels"`
	ChangeProposal *ChangeProposalState `json:"change_proposal,omitempty"`
}

// ChangeProposalState carries MR/PR metadata needed by stages.
type ChangeProposalState struct {
	ID       int    `json:"id"`
	HeadRepo string `json:"head_repo"`
	BaseRepo string `json:"base_repo"`
	HeadRef  string `json:"head_ref"`
	BaseRef  string `json:"base_ref"`
	HeadSHA  string `json:"head_sha,omitempty"`
	AuthorID string `json:"author_id"`
	IsFork   bool   `json:"is_fork"`
}

// Source records event provenance.
type Source struct {
	System    string `json:"system"`
	RawType   string `json:"raw_type"`
	RawAction string `json:"raw_action,omitempty"`
}

// EventRouter routes a NormalizedEvent to zero or more stage names.
type EventRouter interface {
	Route(event *NormalizedEvent) ([]string, error)
}
