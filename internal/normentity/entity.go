package normentity

// Timestamp is a UTC string in `YYYY-MM-DDTHH:MM:SS.nnnnnnnnnZ` form (exactly
// nine fractional digits), so lexical comparison equals chronological
// comparison, including in CEL. It is deliberately a string, not time.Time.
type Timestamp string

// Kind is the entity's domain kind.
type Kind string

// Entity kinds.
const (
	KindWorkItem       Kind = "work_item"
	KindChangeProposal Kind = "change_proposal"
	KindConversation   Kind = "conversation"
)

// State is the portable current entity state.
type State string

// Entity states. Merged applies only to a change proposal.
const (
	StateOpen   State = "open"
	StateClosed State = "closed"
	StateMerged State = "merged"
)

// System is the authoritative source system.
type System string

// Source systems.
const (
	SystemGitHub System = "github"
	SystemGitLab System = "gitlab"
	SystemJira   System = "jira"
)

// ActorKind classifies an actor; Team is used only by reviewer subjects.
type ActorKind string

// Actor kinds.
const (
	ActorHuman ActorKind = "human"
	ActorBot   ActorKind = "bot"
	ActorTeam  ActorKind = "team"
)

// Role is the Authorization Contract role vocabulary.
type Role string

// Roles.
const (
	RoleAdmin    Role = "admin"
	RoleMaintain Role = "maintain"
	RoleWrite    Role = "write"
	RoleTriage   Role = "triage"
	RoleRead     Role = "read"
	RoleNone     Role = "none"
	RoleExternal Role = "external"
)

// ReviewState is a formal review's disposition.
type ReviewState string

// Review states.
const (
	ReviewApproved         ReviewState = "approved"
	ReviewChangesRequested ReviewState = "changes_requested"
	ReviewCommented        ReviewState = "commented"
	ReviewDismissed        ReviewState = "dismissed"
)

// ReviewerState is a reviewer's current standing.
type ReviewerState string

// Reviewer states.
const (
	ReviewerUnreviewed       ReviewerState = "unreviewed"
	ReviewerReviewStarted    ReviewerState = "review_started"
	ReviewerReviewed         ReviewerState = "reviewed"
	ReviewerApproved         ReviewerState = "approved"
	ReviewerChangesRequested ReviewerState = "changes_requested"
	ReviewerUnapproved       ReviewerState = "unapproved"
	ReviewerDismissed        ReviewerState = "dismissed"
)

// ApprovalGate is the current approval requirement result.
type ApprovalGate string

// Approval gate results.
const (
	GateNotRequired ApprovalGate = "not_required"
	GateSatisfied   ApprovalGate = "satisfied"
	GateUnsatisfied ApprovalGate = "unsatisfied"
)

// Side is a diff side.
type Side string

// Diff sides.
const (
	SideLeft  Side = "left"
	SideRight Side = "right"
)

// CheckStatus is a check's execution status.
type CheckStatus string

// Check statuses.
const (
	CheckQueued     CheckStatus = "queued"
	CheckInProgress CheckStatus = "in_progress"
	CheckCompleted  CheckStatus = "completed"
)

// CheckConclusion is a completed check's outcome.
type CheckConclusion string

// Check conclusions.
const (
	ConclusionSuccess        CheckConclusion = "success"
	ConclusionFailure        CheckConclusion = "failure"
	ConclusionNeutral        CheckConclusion = "neutral"
	ConclusionCancelled      CheckConclusion = "cancelled"
	ConclusionSkipped        CheckConclusion = "skipped"
	ConclusionTimedOut       CheckConclusion = "timed_out"
	ConclusionActionRequired CheckConclusion = "action_required"
	ConclusionStale          CheckConclusion = "stale"
)

// AnnotationLevel is a check annotation's severity.
type AnnotationLevel string

// Annotation levels.
const (
	LevelNotice  AnnotationLevel = "notice"
	LevelWarning AnnotationLevel = "warning"
	LevelFailure AnnotationLevel = "failure"
)

// Entity is the source-neutral normalized entity (normalized-entity v1): the
// value a CEL binding exposes as `entity`.
//
// Its shape follows the specification's `entity` object member for member, so
// a CEL adapter can wrap an Entity directly instead of copying it into a
// bridge structure:
//
//   - [Field] carries the absent, known, null, and unknown states of
//     attributes that can be unresolved (to become CEL unknown);
//   - [List] carries a collection's known members together with the coverage
//     of its remainder (to become a coverage-aware CEL list);
//   - members that do not apply to the entity's kind are zero and omitted
//     (absent, following ordinary CEL missing-key behavior).
//
// Required members are always serialized; the JSON Schema remains the
// authority on which members are required for which kind and provider.
type Entity struct {
	Kind     Kind   `json:"kind"`
	Source   Source `json:"source"`
	ID       ID     `json:"id"`
	SourceID string `json:"source_id"`
	Key      string `json:"key"`
	Number   int    `json:"number"`
	URL      string `json:"url"`

	State     Field[State]     `json:"state,omitzero"`
	Title     Field[string]    `json:"title,omitzero"`
	Author    Field[Actor]     `json:"author,omitzero"`
	CreatedAt Field[Timestamp] `json:"created_at,omitzero"`
	UpdatedAt Field[Timestamp] `json:"updated_at,omitzero"`

	Labels     List[string] `json:"labels,omitzero"`
	Assignees  List[Actor]  `json:"assignees,omitzero"`
	Lifecycle  Lifecycle    `json:"lifecycle"`
	Extensions *Extensions  `json:"extensions,omitempty"`

	// Kind-specific sections: WorkItem applies only to a work item,
	// ChangeProposal (with Reviews, Reviewers, CurrentApprovals, and Checks)
	// only to a change proposal, Conversation only to a conversation.
	WorkItem       Field[WorkItem]       `json:"work_item,omitzero"`
	ChangeProposal Field[ChangeProposal] `json:"change_proposal,omitzero"`
	Conversation   Field[Conversation]   `json:"conversation,omitzero"`

	// UnresolvedChangeProposal holds the coverage of the change proposal's
	// changed_files and commits collections while ChangeProposal is not
	// known (an unknown marker). The specification still requires both
	// coverage entries then, but there are no [List] values to carry them.
	// It is ignored once ChangeProposal is known, where each [List] carries
	// its own coverage. It is host-only and never serialized in the entity.
	UnresolvedChangeProposal ChangeProposalCoverage `json:"-"`

	// Body is the body action, null when the source confirms no body, or
	// unknown. It is singular and has no coverage.
	Body Field[TextAction] `json:"body,omitzero"`

	Comments          List[Comment]          `json:"comments,omitzero"`
	Reviews           List[Review]           `json:"reviews,omitzero"`
	Reviewers         List[Reviewer]         `json:"reviewers,omitzero"`
	CurrentApprovals  List[CurrentApproval]  `json:"current_approvals,omitzero"`
	LabelApplications List[LabelApplication] `json:"label_applications,omitzero"`
	Threads           List[Thread]           `json:"threads,omitzero"`
	Reactions         List[Reaction]         `json:"reactions,omitzero"`
	Checks            List[Check]            `json:"checks,omitzero"`
}

// Source identifies the authoritative system and container of an entity.
// Forge entities set Repository and RepositoryID; Jira entities set Project
// and ProjectID.
type Source struct {
	System       System `json:"system"`
	Host         string `json:"host"`
	Repository   string `json:"repository,omitempty"`
	RepositoryID string `json:"repository_id,omitempty"`
	Project      string `json:"project,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
}

// Actor is the shared ADR 0107 person shape used for authors, assignees,
// action actors, lifecycle actors, resolvers, reaction actors, approvers, and
// actor-valued extensions. Every attribute can be unresolved. An admitted
// actor is the same shape with a concrete ID, kind, and role and
// RoleVerified true. A human omits BotRole.
type Actor struct {
	ID           Field[string]    `json:"id,omitzero"`
	Login        Field[string]    `json:"login,omitzero"`
	Kind         Field[ActorKind] `json:"kind,omitzero"`
	Role         Field[Role]      `json:"role,omitzero"`
	BotRole      Field[string]    `json:"bot_role,omitzero"`
	RoleVerified Field[bool]      `json:"role_verified,omitzero"`
}

// Lifecycle records source-backed close and merge times and actors. The
// merge members apply only to a change proposal.
type Lifecycle struct {
	ClosedAt Field[Timestamp] `json:"closed_at,omitzero"`
	ClosedBy Field[Actor]     `json:"closed_by,omitzero"`
	MergedAt Field[Timestamp] `json:"merged_at,omitzero"`
	MergedBy Field[Actor]     `json:"merged_by,omitzero"`
}

// WorkItem holds work-item-only source details.
type WorkItem struct {
	SourceStatus Field[SourceStatus] `json:"source_status,omitzero"`
	IssueType    Field[IssueType]    `json:"issue_type,omitzero"`
	StateReason  Field[string]       `json:"state_reason,omitzero"`
}

// SourceStatus is the source-native status name and category.
type SourceStatus struct {
	Name     Field[string] `json:"name,omitzero"`
	Category Field[string] `json:"category,omitzero"`
}

// IssueType is the source-native issue type.
type IssueType struct {
	ID        Field[string] `json:"id,omitzero"`
	Name      Field[string] `json:"name,omitzero"`
	IsSubtask Field[bool]   `json:"is_subtask,omitzero"`
}

// ChangeProposal holds change-proposal-only details.
type ChangeProposal struct {
	BaseRepository Field[string]     `json:"base_repository,omitzero"`
	HeadRepository Field[string]     `json:"head_repository,omitzero"`
	BaseRef        Field[string]     `json:"base_ref,omitzero"`
	HeadRef        Field[string]     `json:"head_ref,omitzero"`
	HeadSHA        Field[string]     `json:"head_sha,omitzero"`
	IsFork         Field[bool]       `json:"is_fork,omitzero"`
	Draft          Field[bool]       `json:"draft,omitzero"`
	Approval       Field[Approval]   `json:"approval,omitzero"`
	ChangedFiles   List[ChangedFile] `json:"changed_files,omitzero"`
	Commits        List[string]      `json:"commits,omitzero"`
}

// ChangeProposalCoverage is the coverage of a change proposal's changed_files
// and commits collections, kept at entity level while the change proposal
// itself is unresolved. See [Entity.UnresolvedChangeProposal].
type ChangeProposalCoverage struct {
	ChangedFiles Coverage
	Commits      Coverage
}

// Approval is the current approval-requirement result.
type Approval struct {
	Gate Field[ApprovalGate] `json:"gate,omitzero"`
}

// ChangedFile is one changed path. Patch is the complete filtered diff,
// null when the source confirms no patch, or unknown.
type ChangedFile struct {
	Path  string        `json:"path"`
	Patch Field[string] `json:"patch,omitzero"`
}

// Conversation holds conversation-only details.
type Conversation struct {
	Category Field[ConversationCategory] `json:"category,omitzero"`
}

// ConversationCategory is a Discussion's exclusive category.
type ConversationCategory struct {
	Name   Field[string] `json:"name,omitzero"`
	ID     Field[string] `json:"id,omitzero"`
	Slug   Field[string] `json:"slug,omitzero"`
	Format Field[string] `json:"format,omitzero"`
}

// Action holds the members shared by body, comment, review, and
// label-application records. Actor must be an admitted actor.
type Action struct {
	ID             string           `json:"id"`
	CreatedAt      Timestamp        `json:"created_at"`
	RevisedAt      Field[Timestamp] `json:"revised_at,omitzero"`
	Actor          Actor            `json:"actor"`
	IsEntityAuthor Field[bool]      `json:"is_entity_author,omitzero"`
	Extensions     *Extensions      `json:"extensions,omitempty"`
}

// TextAction is an action carrying filtered text: the entity body, and the
// base of comments and reviews. Text is the complete filtered current text
// (an empty string is valid) or unknown; Command is the first-line command
// token, null when there is none, or unknown.
type TextAction struct {
	Action
	Text    Field[string] `json:"text,omitzero"`
	Command Field[string] `json:"command,omitzero"`
}

// Comment is a source comment, note, review-thread comment, or Discussion
// reply.
type Comment struct {
	TextAction
	// ThreadID is the source thread ID, null when the source confirms the
	// comment is unthreaded, or unknown.
	ThreadID Field[string] `json:"thread_id,omitzero"`
	// ParentID is the Discussion thread-root ID (equal to ID on a root).
	ParentID string        `json:"parent_id,omitempty"`
	URL      Field[string] `json:"url,omitzero"`
}

// Review is a submitted formal review on a change proposal.
type Review struct {
	TextAction
	ReviewState Field[ReviewState] `json:"review_state,omitzero"`
}

// LabelApplication is the attributable application of a current label.
type LabelApplication struct {
	Action
	Label Field[string] `json:"label,omitzero"`
}

// Thread is a source comment thread. CommentIDs lists known admitted comment
// IDs in source thread order, with its own coverage.
type Thread struct {
	ID         string                `json:"id"`
	CreatedAt  Timestamp             `json:"created_at"`
	CommentIDs List[string]          `json:"comment_ids,omitzero"`
	Resolved   Field[bool]           `json:"resolved,omitzero"`
	Outdated   Field[bool]           `json:"outdated,omitzero"`
	ReviewID   Field[string]         `json:"review_id,omitzero"`
	ResolvedBy Field[Actor]          `json:"resolved_by,omitzero"`
	Location   Field[ThreadLocation] `json:"location,omitzero"`
	Extensions *Extensions           `json:"extensions,omitempty"`
}

// ThreadLocation is a source-backed diff position.
type ThreadLocation struct {
	Path              string      `json:"path"`
	Line              Field[int]  `json:"line,omitzero"`
	Side              Field[Side] `json:"side,omitzero"`
	StartLine         Field[int]  `json:"start_line,omitzero"`
	StartSide         Field[Side] `json:"start_side,omitzero"`
	OriginalLine      Field[int]  `json:"original_line,omitzero"`
	OriginalStartLine Field[int]  `json:"original_start_line,omitzero"`
	OldLine           Field[int]  `json:"old_line,omitzero"`
	NewLine           Field[int]  `json:"new_line,omitzero"`
}

// ReviewerSubject is a reviewer's identity: an actor, or a team (Kind
// ActorTeam with ID and Name, and no other actor members).
type ReviewerSubject struct {
	Actor
	Name Field[string] `json:"name,omitzero"`
}

// Reviewer is one current reviewer assignment or standing.
type Reviewer struct {
	Subject ReviewerSubject      `json:"subject"`
	State   Field[ReviewerState] `json:"state,omitzero"`
}

// CurrentApproval is a currently recorded approval.
type CurrentApproval struct {
	Actor          Actor            `json:"actor"`
	ApprovedAt     Field[Timestamp] `json:"approved_at,omitzero"`
	IsEntityAuthor Field[bool]      `json:"is_entity_author,omitzero"`
}

// Reaction is a source-backed reaction on the entity.
type Reaction struct {
	ID        string           `json:"id"`
	Content   Field[string]    `json:"content,omitzero"`
	Actor     Actor            `json:"actor"`
	CreatedAt Field[Timestamp] `json:"created_at,omitzero"`
}

// Check is a current-head check result.
type Check struct {
	ID           string                 `json:"id"`
	Name         Field[string]          `json:"name,omitzero"`
	Status       Field[CheckStatus]     `json:"status,omitzero"`
	Conclusion   Field[CheckConclusion] `json:"conclusion,omitzero"`
	CreatedAt    Timestamp              `json:"created_at"`
	HeadSHA      string                 `json:"head_sha"`
	StartedAt    Field[Timestamp]       `json:"started_at,omitzero"`
	CompletedAt  Field[Timestamp]       `json:"completed_at,omitzero"`
	NeverStarted Field[bool]            `json:"never_started,omitzero"`
	Log          CheckLog               `json:"log"`
	Report       CheckReport            `json:"report"`
	Extensions   *Extensions            `json:"extensions,omitempty"`
	URL          Field[string]          `json:"url,omitzero"`
	RunAttempt   Field[int]             `json:"run_attempt,omitzero"`
	TriggerEvent Field[string]          `json:"trigger_event,omitzero"`
}

// CheckLog holds a check's log lines read from either end. Each side is a
// contiguous known prefix from its end with independent coverage.
type CheckLog struct {
	Ascending  List[LogEntry] `json:"ascending,omitzero"`
	Descending List[LogEntry] `json:"descending,omitzero"`
}

// LogEntry is one source log line.
type LogEntry struct {
	Text      Field[string]    `json:"text,omitzero"`
	Timestamp Field[Timestamp] `json:"timestamp,omitzero"`
}

// CheckReport is the forge-published check report.
type CheckReport struct {
	Title       Field[string]         `json:"title,omitzero"`
	Summary     Field[string]         `json:"summary,omitzero"`
	Details     Field[string]         `json:"details,omitzero"`
	Annotations List[CheckAnnotation] `json:"annotations,omitzero"`
}

// CheckAnnotation is a forge-published file finding attached to a check.
type CheckAnnotation struct {
	Path        string          `json:"path"`
	StartLine   int             `json:"start_line"`
	EndLine     int             `json:"end_line"`
	StartColumn Field[int]      `json:"start_column,omitzero"`
	EndColumn   Field[int]      `json:"end_column,omitzero"`
	Level       AnnotationLevel `json:"level"`
	Title       Field[string]   `json:"title,omitzero"`
	Message     Field[string]   `json:"message,omitzero"`
	Details     Field[string]   `json:"details,omitzero"`
}
