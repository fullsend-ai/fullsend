package input

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/normevent"
)

// Forgejo Actions event input driver (forgejo-event).
//
// Forgejo Actions writes the triggering event's payload to the file named
// by FORGEJO_EVENT_PATH (GITHUB_EVENT_PATH on older runners). The payload
// follows Forgejo's webhook structures, which resemble GitHub's but differ
// in action names (synchronized, label_updated, label_cleared, reviewed)
// and URL layout (/pulls/N). This driver maps the supported issue,
// comment, label, pull-request and review events onto the NormalizedEvent
// v1 vocabulary (docs/normative/normalized-event/v1/README.md, "Forgejo
// adapter notes").
//
// Every event is bound to the configured instance and repository: the
// payload's repository.full_name must equal the configured repository, and
// every entity URL must sit under <server URL>/<owner>/<repo>/. A payload
// from another forge, another instance or another repository fails rather
// than being mapped. Unsupported event names or actions, missing required
// objects and label actions that do not identify the changed label all
// return an error and no event.
//
// The payload is a candidate event. The actor role comes only from the
// ResolveRole callback (RoleNone when it is unset or fails), so a caller
// without verified Forgejo permissions gets an event the authorization gate
// denies. Re-reading current resource state before dispatch is the
// dispatch path's responsibility, not this mapper's.

// maxForgejoPayloadBytes bounds the event payload the driver reads.
// Oversized payloads fail rather than being truncated.
const maxForgejoPayloadBytes = 8 << 20

// forgejoTextLimit is the code-point cap on comment body and instruction,
// matching the gha-event driver.
const forgejoTextLimit = 4096

// forgejoActionsUserID is the ID Forgejo assigns to its built-in Actions
// user, which authors changes made with the per-run FORGEJO_TOKEN.
const forgejoActionsUserID = -2

// forgejoWIPPrefixes are Forgejo's default work-in-progress title prefixes
// (repository.pull-request WORK_IN_PROGRESS_PREFIXES), compared
// case-insensitively like Forgejo does.
var forgejoWIPPrefixes = []string{"WIP:", "[WIP]"}

// ForgejoEventOptions configures the Forgejo Actions event input driver.
type ForgejoEventOptions struct {
	// EventPath is the payload file. Default: FORGEJO_EVENT_PATH, then
	// GITHUB_EVENT_PATH.
	EventPath string
	// EventName is the Actions event name (issues, issue_comment,
	// pull_request, pull_request_target, pull_request_review). Default:
	// FORGEJO_EVENT_NAME, then GITHUB_EVENT_NAME.
	EventName string
	// Repository is the configured owner/repo. Default:
	// FORGEJO_REPOSITORY, then GITHUB_REPOSITORY.
	Repository string
	// ServerURL is the configured instance URL, including any path
	// prefix. Default: FORGEJO_SERVER_URL, then GITHUB_SERVER_URL.
	ServerURL string
	// ResolveRole returns the actor's verified repository role. When nil,
	// or when it fails, the actor role is RoleNone (fail closed).
	ResolveRole func(ctx context.Context, login string) (normevent.ActorRole, error)
}

// LoadForgejoEvent reads the Forgejo Actions event payload and maps it to
// a validated NormalizedEvent. Unset options fall back to the FORGEJO_*
// and then GITHUB_* runner environment variables.
func LoadForgejoEvent(ctx context.Context, opts ForgejoEventOptions) (*normevent.Event, error) {
	opts.EventPath = firstNonEmpty(opts.EventPath, os.Getenv("FORGEJO_EVENT_PATH"), os.Getenv("GITHUB_EVENT_PATH"))
	opts.EventName = firstNonEmpty(opts.EventName, os.Getenv("FORGEJO_EVENT_NAME"), os.Getenv("GITHUB_EVENT_NAME"))
	opts.Repository = firstNonEmpty(opts.Repository, os.Getenv("FORGEJO_REPOSITORY"), os.Getenv("GITHUB_REPOSITORY"))
	opts.ServerURL = firstNonEmpty(opts.ServerURL, os.Getenv("FORGEJO_SERVER_URL"), os.Getenv("GITHUB_SERVER_URL"))
	if opts.EventPath == "" {
		return nil, errors.New("forgejo event: FORGEJO_EVENT_PATH is not set")
	}

	f, err := os.Open(opts.EventPath)
	if err != nil {
		return nil, fmt.Errorf("forgejo event: reading event file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxForgejoPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("forgejo event: reading event file: %w", err)
	}
	return MapForgejoEvent(ctx, opts, data)
}

// MapForgejoEvent maps a Forgejo Actions event payload to a validated
// NormalizedEvent. It does not read the environment.
func MapForgejoEvent(ctx context.Context, opts ForgejoEventOptions, data []byte) (*normevent.Event, error) {
	if len(data) > maxForgejoPayloadBytes {
		return nil, fmt.Errorf("forgejo event: payload exceeds %d bytes", maxForgejoPayloadBytes)
	}
	if strings.TrimSpace(opts.EventName) == "" {
		return nil, errors.New("forgejo event: event name is not set")
	}
	repo, err := forgejoRepository(opts.Repository)
	if err != nil {
		return nil, err
	}
	server, err := forgejoServerURL(opts.ServerURL)
	if err != nil {
		return nil, err
	}

	var p forgejoPayload
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("forgejo event: parsing %s payload: %w", opts.EventName, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("forgejo event: %s payload has trailing data", opts.EventName)
	}
	if p.Repository == nil || strings.TrimSpace(p.Repository.FullName) == "" {
		return nil, fmt.Errorf("forgejo event: %s payload is missing repository.full_name", opts.EventName)
	}
	if !strings.EqualFold(p.Repository.FullName, repo) {
		return nil, fmt.Errorf("forgejo event: payload repository %q does not match configured repository %q", p.Repository.FullName, repo)
	}
	if p.Sender == nil || strings.TrimSpace(p.Sender.Login) == "" {
		return nil, fmt.Errorf("forgejo event: %s payload is missing sender.login", opts.EventName)
	}
	if strings.TrimSpace(p.Action) == "" {
		return nil, fmt.Errorf("forgejo event: %s payload is missing action", opts.EventName)
	}

	m := forgejoMapper{
		eventName: opts.EventName,
		repo:      repo,
		urlPrefix: server + "/" + repo + "/",
		p:         &p,
	}
	ev := &normevent.Event{
		Repo: repo,
		Actor: normevent.Actor{
			ID:   p.Sender.Login,
			Kind: forgejoActorKind(p.Sender),
			Role: resolveForgejoRole(ctx, opts.ResolveRole, p.Sender.Login, repo),
		},
		Source: normevent.Source{
			System:    normevent.SystemForgejo,
			RawType:   opts.EventName,
			RawAction: p.Action,
		},
		State: normevent.State{Labels: []string{}},
	}

	switch opts.EventName {
	case "issues":
		err = m.issues(ev)
	case "issue_comment":
		err = m.issueComment(ev)
	case "pull_request", "pull_request_target":
		err = m.pullRequest(ev)
	case "pull_request_review":
		err = m.pullRequestReview(ev)
	default:
		return nil, fmt.Errorf("forgejo event: unsupported event name %q", opts.EventName)
	}
	if err != nil {
		return nil, err
	}
	if err := ev.Validate(); err != nil {
		return nil, fmt.Errorf("forgejo event: %w", err)
	}
	return ev, nil
}

type forgejoUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type forgejoLabel struct {
	Name string `json:"name"`
}

type forgejoRepo struct {
	FullName string `json:"full_name"`
}

type forgejoPullRequestMeta struct {
	HTMLURL string `json:"html_url"`
}

type forgejoIssue struct {
	Number      int                     `json:"number"`
	HTMLURL     string                  `json:"html_url"`
	User        *forgejoUser            `json:"user"`
	Labels      []forgejoLabel          `json:"labels"`
	Assignee    *forgejoUser            `json:"assignee"`
	Assignees   []forgejoUser           `json:"assignees"`
	PullRequest *forgejoPullRequestMeta `json:"pull_request"`
}

type forgejoBranch struct {
	Ref  string       `json:"ref"`
	SHA  string       `json:"sha"`
	Repo *forgejoRepo `json:"repo"`
}

type forgejoPullRequest struct {
	Number    int            `json:"number"`
	HTMLURL   string         `json:"html_url"`
	Title     string         `json:"title"`
	User      *forgejoUser   `json:"user"`
	Labels    []forgejoLabel `json:"labels"`
	Assignee  *forgejoUser   `json:"assignee"`
	Assignees []forgejoUser  `json:"assignees"`
	Head      *forgejoBranch `json:"head"`
	Base      *forgejoBranch `json:"base"`
	Merged    bool           `json:"merged"`
	Draft     *bool          `json:"draft"`
}

type forgejoComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type forgejoReview struct {
	Type  string `json:"type"`
	State string `json:"state"`
}

type forgejoChangeFrom struct {
	From string `json:"from"`
}

type forgejoChanges struct {
	Title *forgejoChangeFrom `json:"title"`
}

// forgejoPayload is the subset of Forgejo's IssuePayload,
// IssueCommentPayload and PullRequestPayload the driver reads.
type forgejoPayload struct {
	Action      string              `json:"action"`
	Repository  *forgejoRepo        `json:"repository"`
	Sender      *forgejoUser        `json:"sender"`
	Issue       *forgejoIssue       `json:"issue"`
	PullRequest *forgejoPullRequest `json:"pull_request"`
	Comment     *forgejoComment     `json:"comment"`
	Label       *forgejoLabel       `json:"label"`
	Review      *forgejoReview      `json:"review"`
	Changes     *forgejoChanges     `json:"changes"`
	IsPull      bool                `json:"is_pull"`
}

type forgejoMapper struct {
	eventName string
	repo      string
	urlPrefix string
	p         *forgejoPayload
}

func (m forgejoMapper) errorf(format string, args ...any) error {
	return fmt.Errorf("forgejo event: %s/%s: %s", m.eventName, m.p.Action, fmt.Sprintf(format, args...))
}

// boundURL checks that rawURL names a resource of the configured
// repository on the configured instance.
func (m forgejoMapper) boundURL(what, rawURL string) error {
	if strings.TrimSpace(rawURL) == "" {
		return m.errorf("%s is missing html_url", what)
	}
	if len(rawURL) <= len(m.urlPrefix) || !strings.EqualFold(rawURL[:len(m.urlPrefix)], m.urlPrefix) {
		return m.errorf("%s URL is not under the configured repository on the configured Forgejo instance", what)
	}
	return nil
}

func (m forgejoMapper) issues(ev *normevent.Event) error {
	issue := m.p.Issue
	if issue == nil {
		return m.errorf("payload is missing issue")
	}
	if issue.PullRequest != nil {
		return m.errorf("issue is a pull request; Forgejo delivers pull request activity as pull_request events")
	}
	if err := m.workItem(ev, issue); err != nil {
		return err
	}

	switch m.p.Action {
	case "opened":
		ev.Transition.Kind = normevent.TransitionOpened
	case "reopened":
		ev.Transition.Kind = normevent.TransitionReopened
	case "edited":
		ev.Transition.Kind = normevent.TransitionEdited
	case "closed":
		ev.Transition.Kind = normevent.TransitionClosed
	case "labeled", "unlabeled", "label_updated", "label_cleared":
		change, err := m.labelChange(ev.State.Labels)
		if err != nil {
			return err
		}
		ev.Transition.Kind = normevent.TransitionLabelChanged
		ev.Transition.Label = change
	case "assigned", "unassigned", "milestoned", "demilestoned":
		ev.Transition.Kind = normevent.TransitionUpdated
	default:
		return m.errorf("unsupported issues action")
	}
	return nil
}

func (m forgejoMapper) issueComment(ev *normevent.Event) error {
	issue := m.p.Issue
	if issue == nil {
		return m.errorf("payload is missing issue")
	}
	comment := m.p.Comment
	if comment == nil {
		return m.errorf("payload is missing comment")
	}
	if comment.ID < 1 {
		return m.errorf("comment.id must be a positive integer")
	}
	if err := m.workItem(ev, issue); err != nil {
		return err
	}

	if m.p.IsPull || issue.PullRequest != nil {
		prURL := ""
		if issue.PullRequest != nil {
			prURL = issue.PullRequest.HTMLURL
		}
		if prURL == "" {
			prURL = strings.Replace(issue.HTMLURL, "/issues/", "/pulls/", 1)
		}
		if err := m.boundURL("pull request", prURL); err != nil {
			return err
		}
		ev.Entity.LinkedChangeProposal = &normevent.LinkedChangeProposal{ID: issue.Number, URL: prURL}
		if pr := m.p.PullRequest; pr != nil {
			if pr.Number != issue.Number {
				return m.errorf("pull_request.number %d does not match issue.number %d", pr.Number, issue.Number)
			}
			cp, err := m.changeProposal(pr)
			if err != nil {
				return err
			}
			ev.State.ChangeProposal = cp
		}
	}

	body := comment.Body
	c := &normevent.Comment{
		ID:   strconv.FormatInt(comment.ID, 10),
		Body: truncateRunes(body, forgejoTextLimit),
	}
	switch m.p.Action {
	case "created":
		// Slash commands are parsed only from newly created comments.
		cmd, instr := extractCommentCommand(body)
		c.Command = cmd
		c.Instruction = truncateRunes(instr, forgejoTextLimit)
		ev.Transition.Kind = normevent.TransitionCommentAdded
	case "edited":
		ev.Transition.Kind = normevent.TransitionCommentEdited
	case "deleted":
		ev.Transition.Kind = normevent.TransitionCommentDeleted
	default:
		return m.errorf("unsupported issue_comment action")
	}
	ev.Transition.Comment = c
	return nil
}

func (m forgejoMapper) pullRequest(ev *normevent.Event) error {
	pr := m.p.PullRequest
	if pr == nil {
		return m.errorf("payload is missing pull_request")
	}
	if err := m.changeProposalEntity(ev, pr, ev.Actor.ID); err != nil {
		return err
	}

	switch m.p.Action {
	case "opened":
		ev.Transition.Kind = normevent.TransitionOpened
	case "reopened":
		ev.Transition.Kind = normevent.TransitionReopened
	case "synchronized", "synchronize":
		ev.Transition.Kind = normevent.TransitionSynchronized
	case "ready_for_review":
		ev.Transition.Kind = normevent.TransitionMarkedReady
	case "edited":
		ev.Transition.Kind = normevent.TransitionEdited
		if m.removedWIPPrefix(pr) {
			ev.Transition.Kind = normevent.TransitionMarkedReady
		}
	case "closed":
		ev.Transition.Kind = normevent.TransitionClosed
		if pr.Merged {
			ev.Transition.Kind = normevent.TransitionMerged
		}
	case "labeled", "unlabeled", "label_updated", "label_cleared":
		change, err := m.labelChange(ev.State.Labels)
		if err != nil {
			return err
		}
		ev.Transition.Kind = normevent.TransitionLabelChanged
		ev.Transition.Label = change
	case "assigned", "unassigned", "milestoned", "demilestoned", "review_requested", "review_request_removed":
		ev.Transition.Kind = normevent.TransitionUpdated
	default:
		return m.errorf("unsupported pull_request action")
	}
	return nil
}

func (m forgejoMapper) pullRequestReview(ev *normevent.Event) error {
	pr := m.p.PullRequest
	if pr == nil {
		return m.errorf("payload is missing pull_request")
	}
	switch m.p.Action {
	case "reviewed", "submitted":
	default:
		return m.errorf("unsupported pull_request_review action")
	}
	if m.p.Review == nil {
		return m.errorf("payload is missing review")
	}
	state, err := forgejoReviewState(m.p.Review)
	if err != nil {
		return m.errorf("%v", err)
	}
	if err := m.changeProposalEntity(ev, pr, ev.Actor.ID); err != nil {
		return err
	}
	ev.Transition.Kind = normevent.TransitionReviewSubmitted
	ev.Transition.Review = &normevent.Review{State: state, ReviewerID: ev.Actor.ID}
	return nil
}

// workItem populates entity, labels and author/assignee facts from an
// issue object.
func (m forgejoMapper) workItem(ev *normevent.Event, issue *forgejoIssue) error {
	if issue.Number < 1 {
		return m.errorf("issue.number must be a positive integer")
	}
	if err := m.boundURL("issue", issue.HTMLURL); err != nil {
		return err
	}
	ev.Entity = normevent.Entity{Kind: normevent.EntityWorkItem, ID: issue.Number, URL: issue.HTMLURL}
	ev.State.Labels = forgejoLabelNames(issue.Labels)
	ev.Actor.IsEntityAuthor = issue.User != nil && issue.User.Login != "" && strings.EqualFold(issue.User.Login, ev.Actor.ID)
	ev.Actor.IsEntityAssignee = forgejoIsAssignee(issue.Assignees, issue.Assignee, ev.Actor.ID)
	return nil
}

// changeProposalEntity populates entity, labels, change-proposal state and
// author/assignee facts from a pull request object.
func (m forgejoMapper) changeProposalEntity(ev *normevent.Event, pr *forgejoPullRequest, actorID string) error {
	cp, err := m.changeProposal(pr)
	if err != nil {
		return err
	}
	ev.Entity = normevent.Entity{Kind: normevent.EntityChangeProposal, ID: pr.Number, URL: pr.HTMLURL}
	ev.State.Labels = forgejoLabelNames(pr.Labels)
	ev.State.ChangeProposal = cp
	ev.Actor.IsEntityAuthor = cp.AuthorID != "" && strings.EqualFold(cp.AuthorID, actorID)
	ev.Actor.IsEntityAssignee = forgejoIsAssignee(pr.Assignees, pr.Assignee, actorID)
	return nil
}

func (m forgejoMapper) changeProposal(pr *forgejoPullRequest) (*normevent.ChangeProposal, error) {
	if pr.Number < 1 {
		return nil, m.errorf("pull_request.number must be a positive integer")
	}
	if err := m.boundURL("pull request", pr.HTMLURL); err != nil {
		return nil, err
	}
	if pr.Head == nil || strings.TrimSpace(pr.Head.SHA) == "" {
		return nil, m.errorf("pull_request.head.sha is required")
	}
	if pr.Base == nil {
		return nil, m.errorf("pull_request.base is required")
	}
	headRepo, baseRepo := "", ""
	if pr.Head.Repo != nil {
		headRepo = pr.Head.Repo.FullName
	}
	if pr.Base.Repo != nil {
		baseRepo = pr.Base.Repo.FullName
	}
	if baseRepo != "" && !strings.EqualFold(baseRepo, m.repo) {
		return nil, m.errorf("pull_request.base.repo %q does not match configured repository %q", baseRepo, m.repo)
	}
	author := ""
	if pr.User != nil {
		author = pr.User.Login
	}
	return &normevent.ChangeProposal{
		ID:       pr.Number,
		HeadRepo: headRepo,
		BaseRepo: baseRepo,
		HeadRef:  pr.Head.Ref,
		BaseRef:  pr.Base.Ref,
		HeadSHA:  pr.Head.SHA,
		AuthorID: author,
		IsFork:   normevent.ComputeChangeProposalIsFork(headRepo, baseRepo),
	}, nil
}

// labelChange derives the label transition. The payload must name the
// changed label. Forgejo's label_updated covers both additions and
// removals, so its direction comes from whether the label is in the
// payload's label snapshot; labeled, unlabeled and label_cleared must agree
// with that snapshot or the event fails.
func (m forgejoMapper) labelChange(labels []string) (*normevent.LabelChange, error) {
	if m.p.Label == nil || strings.TrimSpace(m.p.Label.Name) == "" {
		return nil, m.errorf("payload does not name the changed label")
	}
	name := m.p.Label.Name
	present := slices.Contains(labels, name)
	switch m.p.Action {
	case "labeled":
		if !present {
			return nil, m.errorf("added label is not in the label snapshot")
		}
		return &normevent.LabelChange{Name: name, Action: "added"}, nil
	case "unlabeled", "label_cleared":
		if present {
			return nil, m.errorf("removed label is still in the label snapshot")
		}
		return &normevent.LabelChange{Name: name, Action: "removed"}, nil
	default: // label_updated
		if present {
			return &normevent.LabelChange{Name: name, Action: "added"}, nil
		}
		return &normevent.LabelChange{Name: name, Action: "removed"}, nil
	}
}

// removedWIPPrefix reports whether an edit took the pull request out of
// draft by removing a default work-in-progress title prefix. It requires
// the payload to state draft: false explicitly.
func (m forgejoMapper) removedWIPPrefix(pr *forgejoPullRequest) bool {
	if pr.Draft == nil || *pr.Draft || m.p.Changes == nil || m.p.Changes.Title == nil {
		return false
	}
	return hasForgejoWIPPrefix(m.p.Changes.Title.From) && !hasForgejoWIPPrefix(pr.Title)
}

func hasForgejoWIPPrefix(title string) bool {
	upper := strings.ToUpper(strings.TrimSpace(title))
	for _, prefix := range forgejoWIPPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// forgejoReviewState maps a Forgejo review payload to the normalized
// review state. Forgejo sends the review hook type (for example
// pull_request_review_approved) in review.type; review.state is accepted
// for payloads that carry an API review state instead.
func forgejoReviewState(r *forgejoReview) (string, error) {
	raw := r.Type
	if raw == "" {
		raw = r.State
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "pull_request_review_approved", "approved":
		return "approved", nil
	case "pull_request_review_rejected", "rejected", "request_changes", "changes_requested":
		return "changes_requested", nil
	case "pull_request_review_comment", "comment", "commented":
		return "commented", nil
	case "":
		return "", errors.New("review type is missing")
	default:
		return "", fmt.Errorf("unsupported review type %q", raw)
	}
}

func forgejoActorKind(u *forgejoUser) normevent.ActorKind {
	if u.ID == forgejoActionsUserID || strings.EqualFold(u.Login, "forgejo-actions") {
		return normevent.ActorBot
	}
	return normevent.ActorHuman
}

func resolveForgejoRole(ctx context.Context, resolve func(context.Context, string) (normevent.ActorRole, error), login, repo string) normevent.ActorRole {
	if resolve == nil {
		return normevent.RoleNone
	}
	role, err := resolve(ctx, login)
	if err != nil {
		log.Printf("harness dispatch: forgejo permission lookup failed for %s on %s: %v", login, repo, err)
		return normevent.RoleNone
	}
	if role == "" {
		return normevent.RoleNone
	}
	return role
}

func forgejoLabelNames(labels []forgejoLabel) []string {
	names := []string{}
	for _, l := range labels {
		if l.Name != "" {
			names = append(names, l.Name)
		}
	}
	return names
}

// forgejoIsAssignee mirrors isAssignee: the assignees array wins, and the
// legacy single assignee is consulted only when the array is absent.
func forgejoIsAssignee(assignees []forgejoUser, assignee *forgejoUser, actorID string) bool {
	if actorID == "" {
		return false
	}
	if assignees != nil {
		for _, a := range assignees {
			if strings.EqualFold(a.Login, actorID) {
				return true
			}
		}
		return false
	}
	return assignee != nil && strings.EqualFold(assignee.Login, actorID)
}

func forgejoRepository(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", errors.New("forgejo event: FORGEJO_REPOSITORY is not set")
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(repo, "..") {
		return "", fmt.Errorf("forgejo event: repository %q must be owner/repo", repo)
	}
	return repo, nil
}

func forgejoServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("forgejo event: FORGEJO_SERVER_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("forgejo event: parsing server URL: %w", err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("forgejo event: server URL must be an http(s) URL with a host and no query, fragment or credentials")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
