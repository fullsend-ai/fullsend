package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/fullsend-ai/fullsend/internal/entityacquire"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normentity"
)

// entityPageSize is the GitHub GraphQL connection page size (its maximum).
const entityPageSize = 100

// entityLimitCursor is the synthetic continuation returned with a page whose
// collection hit a provider result cap. Reading it reports the limit. It is
// not a GitHub cursor (those are opaque base64 strings).
const entityLimitCursor = "fullsend:result-cap"

// EntityAdapter is the GitHub issue and pull request provider adapter for
// selective entity acquisition (ADR 0147). It implements
// [entityacquire.Adapter] for one candidate entity, translating the
// provider-neutral needs into GitHub GraphQL reads:
//
//   - [EntityAdapter.ReadBase] reads identity and the requested scalar fact
//     groups in one query. Groups that were not requested are excluded from
//     the query with @include directives, so they are never read.
//   - [EntityAdapter.ReadPage] reads one page of one collection connection.
//
// Every read also returns the entity's updatedAt (and, for a pull request,
// its head commit), which is the source revision. Acquire rejects evidence
// read at different revisions.
//
// The adapter never applies a narrowing filter, so every page it returns is
// unfiltered. It reads labels, assignees, reactions, label applications, and
// a pull request's changed files and commits. Other collections, and text the
// content-safety filter must process first (title and body), are reported
// unsupported, which can never read as empty. Actor roles are left unknown
// for the authorized entity view (#9584) to resolve.
//
// An EntityAdapter is not safe for concurrent use.
type EntityAdapter struct {
	c      *LiveClient
	owner  string
	repo   string
	number int

	// sourceID and kind are established by ReadBase. Every page must read
	// the same entity.
	sourceID string
	kind     normentity.Kind
	// seen counts the members returned in each collection's current read
	// sequence, to detect a provider result cap on the last page.
	seen map[entityacquire.Need]int
	// capped holds the limit error to report when a collection's final page
	// exposed a provider result cap.
	capped map[entityacquire.Need]error
	// labelEvents records the labels whose most recent label event has been
	// read in the current label-application read sequence.
	labelEvents map[string]bool
}

var _ entityacquire.Adapter = (*EntityAdapter)(nil)

// NewEntityAdapter returns the provider adapter for issue or pull request
// number in owner/repo.
func NewEntityAdapter(c *LiveClient, owner, repo string, number int) (*EntityAdapter, error) {
	if c == nil {
		return nil, errors.New("github entity adapter: no client")
	}
	if owner == "" || repo == "" {
		return nil, errors.New("github entity adapter: owner and repository are required")
	}
	if number <= 0 {
		return nil, fmt.Errorf("github entity adapter: invalid entity number %d", number)
	}
	return &EntityAdapter{c: c, owner: owner, repo: repo, number: number, seen: map[entityacquire.Need]int{}, capped: map[entityacquire.Need]error{}, labelEvents: map[string]bool{}}, nil
}

// gqlEntityActor is an Actor selected with entityActorFragment.
type gqlEntityActor struct {
	Typename   string `json:"__typename"`
	Login      string `json:"login"`
	DatabaseID *int64 `json:"databaseId"`
}

const entityActorFragment = `fragment entityActor on Actor {
	__typename
	login
	... on User { databaseId }
	... on Bot { databaseId }
}`

const entityBaseQuery = `query($owner: String!, $name: String!, $number: Int!, $author: Boolean!, $lifecycle: Boolean!, $workItem: Boolean!, $changeProposal: Boolean!) {
	repository(owner: $owner, name: $name) {
		id
		nameWithOwner
		issueOrPullRequest(number: $number) {
			__typename
			... on Issue {
				id
				number
				url
				issueState: state
				createdAt
				updatedAt
				stateReason @include(if: $workItem)
				closedAt @include(if: $lifecycle)
				author @include(if: $author) { ...entityActor }
			}
			... on PullRequest {
				id
				number
				url
				pullRequestState: state
				createdAt
				updatedAt
				headRefOid
				closedAt @include(if: $lifecycle)
				mergedAt @include(if: $lifecycle)
				mergedBy @include(if: $lifecycle) { ...entityActor }
				author @include(if: $author) { ...entityActor }
				baseRefName @include(if: $changeProposal)
				headRefName @include(if: $changeProposal)
				isCrossRepository @include(if: $changeProposal)
				isDraft @include(if: $changeProposal)
				baseRepository @include(if: $changeProposal) { nameWithOwner }
				headRepository @include(if: $changeProposal) { nameWithOwner }
			}
		}
	}
}
` + entityActorFragment

type gqlEntityBase struct {
	Typename string `json:"__typename"`
	ID       string `json:"id"`
	Number   int    `json:"number"`
	URL      string `json:"url"`
	// IssueState and PullRequestState are aliases of state: GitHub types
	// the two as different enums, so one response name cannot serve both.
	IssueState       string `json:"issueState"`
	PullRequestState string `json:"pullRequestState"`
	// State is the alias matching the entity's kind, set by ReadBase.
	State             string          `json:"-"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
	HeadRefOid        string          `json:"headRefOid"`
	StateReason       *string         `json:"stateReason"`
	ClosedAt          *string         `json:"closedAt"`
	MergedAt          *string         `json:"mergedAt"`
	MergedBy          *gqlEntityActor `json:"mergedBy"`
	Author            *gqlEntityActor `json:"author"`
	BaseRefName       string          `json:"baseRefName"`
	HeadRefName       string          `json:"headRefName"`
	IsCrossRepository bool            `json:"isCrossRepository"`
	IsDraft           bool            `json:"isDraft"`
	BaseRepository    *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"baseRepository"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
}

// ReadBase reads the entity's identity and the requested fact groups.
func (a *EntityAdapter) ReadBase(ctx context.Context, facts []entityacquire.Need) (*entityacquire.Base, error) {
	want := func(n entityacquire.Need) bool { return slices.Contains(facts, n) }
	var out struct {
		Data struct {
			Repository *struct {
				ID                 string         `json:"id"`
				NameWithOwner      string         `json:"nameWithOwner"`
				IssueOrPullRequest *gqlEntityBase `json:"issueOrPullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	err := a.query(ctx, entityBaseQuery, map[string]any{
		"owner":          a.owner,
		"name":           a.repo,
		"number":         a.number,
		"author":         want(entityacquire.NeedAuthor),
		"lifecycle":      want(entityacquire.NeedLifecycle),
		"workItem":       want(entityacquire.NeedWorkItem),
		"changeProposal": want(entityacquire.NeedChangeProposal),
	}, &out)
	if err != nil {
		return nil, fmt.Errorf("github entity base read: %w", err)
	}
	r := out.Data.Repository
	if r == nil || r.IssueOrPullRequest == nil {
		return nil, fmt.Errorf("github entity base read: %s/%s#%d: %w", a.owner, a.repo, a.number, forge.ErrNotFound)
	}
	g := r.IssueOrPullRequest
	kind, err := entityKind(g.Typename)
	if err != nil {
		return nil, fmt.Errorf("github entity base read: %w", err)
	}
	if g.ID == "" || r.ID == "" || r.NameWithOwner == "" || g.Number != a.number {
		return nil, fmt.Errorf("github entity base read: %s/%s#%d: incomplete identity", a.owner, a.repo, a.number)
	}
	u, err := url.Parse(g.URL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("github entity base read: invalid entity URL %q", g.URL)
	}
	host := strings.ToLower(u.Host)
	id, err := normentity.NewID(string(normentity.SystemGitHub), host, string(kind), g.ID)
	if err != nil {
		return nil, fmt.Errorf("github entity base read: %w", err)
	}

	e := &normentity.Entity{
		Kind: kind,
		Source: normentity.Source{
			System:       normentity.SystemGitHub,
			Host:         host,
			Repository:   strings.ToLower(r.NameWithOwner),
			RepositoryID: r.ID,
		},
		ID:       id,
		SourceID: g.ID,
		Key:      "#" + strconv.Itoa(g.Number),
		Number:   g.Number,
		URL:      g.URL,
	}
	rawState := g.IssueState
	if kind == normentity.KindChangeProposal {
		rawState = g.PullRequestState
	}
	g.State = rawState
	state := entityState(kind, rawState)
	for _, n := range facts {
		if !n.AppliesTo(kind) {
			continue
		}
		switch n {
		case entityacquire.NeedState:
			e.State = state
		case entityacquire.NeedTitle:
			// Concrete title text must pass the content-safety filter
			// pipeline, which this adapter does not run.
			e.Title = normentity.Unknown[string](normentity.ReasonUnsupported)
		case entityacquire.NeedAuthor:
			e.Author = entityActor(g.Author)
		case entityacquire.NeedCreatedAt:
			e.CreatedAt = entityTimestamp(g.CreatedAt)
		case entityacquire.NeedUpdatedAt:
			e.UpdatedAt = entityTimestamp(g.UpdatedAt)
		case entityacquire.NeedLifecycle:
			e.Lifecycle = entityLifecycle(kind, state, g)
		case entityacquire.NeedBody:
			// Body text needs the content-safety filter and the current
			// editor's provenance.
			e.Body = normentity.Unknown[normentity.TextAction](normentity.ReasonUnsupported)
		case entityacquire.NeedWorkItem:
			e.WorkItem = entityWorkItem(g)
		case entityacquire.NeedChangeProposal:
			e.ChangeProposal = entityChangeProposal(g)
		case entityacquire.NeedExtensions:
			// No extension keys are selected, so the entity carries none.
		}
	}
	a.sourceID, a.kind = g.ID, kind
	return &entityacquire.Base{Entity: e, Revision: entityRevision(kind, g.UpdatedAt, g.HeadRefOid)}, nil
}

// entityCollection describes how one collection is read.
type entityCollection struct {
	// field is the GraphQL connection field.
	field string
	// nodes is the node selection.
	nodes string
	// counted: the connection reports totalCount, which the last page must
	// match. GitHub caps some connections (pull request commits and
	// files) without reporting a next page.
	counted bool
	// args holds connection arguments other than the page window.
	args string
	// backward pages from the newest member to the oldest.
	backward bool
	// singlePage: changes to the collection do not advance the source
	// revision, so only a read that fits in one page (one GraphQL response)
	// is consistent. A longer read reports a limit after its first page.
	singlePage bool
}

// entityActorSelection selects an Actor with the fields entityActor maps.
const entityActorSelection = `{ __typename login ... on User { databaseId } ... on Bot { databaseId } }`

var entityCollections = map[entityacquire.Need]entityCollection{
	entityacquire.NeedLabels: {field: "labels", nodes: "name", counted: true},
	entityacquire.NeedAssignees: {field: "assignedActors", nodes: `__typename
		... on User { login databaseId }
		... on Bot { login databaseId }`},
	entityacquire.NeedChangedFiles: {field: "files", nodes: "path", counted: true},
	entityacquire.NeedCommits:      {field: "commits", nodes: "commit { oid }", counted: true},
	// GitHub does not advance an issue's or pull request's updatedAt when a
	// reaction is added or removed. Reaction.user is a User, so a bot or
	// deleted reactor is null and stays unresolved.
	entityacquire.NeedReactions: {field: "reactions", nodes: "id content createdAt user { __typename login databaseId }", counted: true, singlePage: true},
	// Label applications are read newest first, so the first event seen for
	// a label is its most recent one.
	entityacquire.NeedLabelApplications: {
		field: "timelineItems", args: "itemTypes: [LABELED_EVENT, UNLABELED_EVENT], ", backward: true,
		nodes: `__typename
			... on LabeledEvent { id createdAt label { name } actor ` + entityActorSelection + ` }
			... on UnlabeledEvent { id createdAt label { name } }`,
	},
}

type gqlEntityNode struct {
	gqlEntityActor
	Name   string `json:"name"`
	Path   string `json:"path"`
	Commit *struct {
		OID string `json:"oid"`
	} `json:"commit"`
	ID        string          `json:"id"`
	Content   string          `json:"content"`
	CreatedAt string          `json:"createdAt"`
	User      *gqlEntityActor `json:"user"`
	Actor     *gqlEntityActor `json:"actor"`
	Label     *struct {
		Name string `json:"name"`
	} `json:"label"`
}

// ReadPage reads one page of one collection.
func (a *EntityAdapter) ReadPage(ctx context.Context, req entityacquire.PageRequest) (*entityacquire.Page, error) {
	if a.sourceID == "" {
		return nil, errors.New("github entity page read: base not read")
	}
	if req.Kind != a.kind {
		return nil, fmt.Errorf("github entity page read: kind %s does not match entity kind %s", req.Kind, a.kind)
	}
	coll, ok := entityCollections[req.Need]
	if !ok || !req.Need.AppliesTo(a.kind) {
		return nil, fmt.Errorf("%w: github %s %s", entityacquire.ErrUnsupported, a.kind, req.Need)
	}
	if req.Cursor == entityLimitCursor {
		err, ok := a.capped[req.Need]
		if !ok {
			return nil, fmt.Errorf("github entity %s read: no provider limit pending", req.Need)
		}
		return nil, err
	}
	delete(a.capped, req.Need)
	typename, revField := "Issue", ""
	if a.kind == normentity.KindChangeProposal {
		typename, revField = "PullRequest", "headRefOid"
	}
	count := ""
	if coll.counted {
		count = "totalCount"
	}
	window, pageInfo := "first: $first, after: $cursor", "hasNextPage endCursor"
	if coll.backward {
		window, pageInfo = "last: $first, before: $cursor", "hasPreviousPage startCursor"
	}
	q := fmt.Sprintf(`query($owner: String!, $name: String!, $number: Int!, $first: Int!, $cursor: String) {
	repository(owner: $owner, name: $name) {
		issueOrPullRequest(number: $number) {
			__typename
			... on %s {
				id
				updatedAt
				%s
				conn: %s(%s%s) {
					%s
					pageInfo { %s }
					nodes { %s }
				}
			}
		}
	}
}`, typename, revField, coll.field, coll.args, window, count, pageInfo, coll.nodes)
	var cursor any
	if req.Cursor != "" {
		cursor = req.Cursor
	}
	var out struct {
		Data struct {
			Repository *struct {
				IssueOrPullRequest *struct {
					Typename   string `json:"__typename"`
					ID         string `json:"id"`
					UpdatedAt  string `json:"updatedAt"`
					HeadRefOid string `json:"headRefOid"`
					Conn       *struct {
						TotalCount *int `json:"totalCount"`
						PageInfo   struct {
							HasNextPage     bool   `json:"hasNextPage"`
							EndCursor       string `json:"endCursor"`
							HasPreviousPage bool   `json:"hasPreviousPage"`
							StartCursor     string `json:"startCursor"`
						} `json:"pageInfo"`
						Nodes []gqlEntityNode `json:"nodes"`
					} `json:"conn"`
				} `json:"issueOrPullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	err := a.query(ctx, q, map[string]any{
		"owner":  a.owner,
		"name":   a.repo,
		"number": a.number,
		"first":  entityPageSize,
		"cursor": cursor,
	}, &out)
	if err != nil {
		return nil, fmt.Errorf("github entity %s read: %w", req.Need, err)
	}
	r := out.Data.Repository
	if r == nil || r.IssueOrPullRequest == nil {
		return nil, fmt.Errorf("github entity %s read: %w", req.Need, forge.ErrNotFound)
	}
	g := r.IssueOrPullRequest
	if g.ID != a.sourceID {
		return nil, fmt.Errorf("github entity %s read: entity %q is not the base entity %q", req.Need, g.ID, a.sourceID)
	}
	if g.Conn == nil {
		return nil, fmt.Errorf("github entity %s read: no %s connection", req.Need, coll.field)
	}
	page := &entityacquire.Page{Revision: entityRevision(a.kind, g.UpdatedAt, g.HeadRefOid)}
	more, next := g.Conn.PageInfo.HasNextPage, g.Conn.PageInfo.EndCursor
	if coll.backward {
		more, next = g.Conn.PageInfo.HasPreviousPage, g.Conn.PageInfo.StartCursor
	}
	if more {
		if next == "" {
			return nil, fmt.Errorf("github entity %s read: next page without a cursor", req.Need)
		}
		page.Next = next
	}
	if req.Cursor == "" {
		a.seen[req.Need] = 0
		if req.Need == entityacquire.NeedLabelApplications {
			clear(a.labelEvents)
		}
	}
	page.Members, err = a.entityMembers(req.Need, g.Conn.Nodes)
	if err != nil {
		return nil, fmt.Errorf("github entity %s read: %w", req.Need, err)
	}

	a.seen[req.Need] += len(g.Conn.Nodes)
	if coll.counted && page.Next == "" {
		total := g.Conn.TotalCount
		if total == nil {
			return nil, fmt.Errorf("github entity %s read: no total count", req.Need)
		}
		if a.seen[req.Need] != *total {
			// Keep the members just read as witnesses: hand them back with a
			// continuation, and report the limit on the following read.
			a.capped[req.Need] = fmt.Errorf("%w: github returned %d of %d %s", entityacquire.ErrLimit, a.seen[req.Need], *total, req.Need)
			page.Next = entityLimitCursor
		}
	}
	if coll.singlePage && page.Next != "" && page.Next != entityLimitCursor {
		// Keep this page's members as witnesses; the rest of the read could
		// not be shown consistent with them.
		a.capped[req.Need] = fmt.Errorf("%w: github %s span more than one page, and changes to them do not advance the source revision", entityacquire.ErrLimit, req.Need)
		page.Next = entityLimitCursor
	}
	return page, nil
}

// entityMembers builds the page fragment for need from the page's nodes.
func (a *EntityAdapter) entityMembers(need entityacquire.Need, nodes []gqlEntityNode) (*normentity.Entity, error) {
	frag := &normentity.Entity{}
	switch need {
	case entityacquire.NeedLabels:
		names := make([]string, 0, len(nodes))
		for _, n := range nodes {
			if n.Name == "" {
				return nil, errors.New("label without a name")
			}
			names = append(names, n.Name)
		}
		frag.Labels.Known = names
	case entityacquire.NeedAssignees:
		actors := make([]normentity.Actor, 0, len(nodes))
		for _, n := range nodes {
			actor := n.gqlEntityActor
			a, _ := entityActor(&actor).Value()
			actors = append(actors, a)
		}
		frag.Assignees.Known = actors
	case entityacquire.NeedChangedFiles:
		files := make([]normentity.ChangedFile, 0, len(nodes))
		for _, n := range nodes {
			if !safeRepoPath(n.Path) {
				return nil, fmt.Errorf("unsafe changed file path %q", n.Path)
			}
			// Patches are acquired separately and pass the content-safety
			// filter, which this adapter does not run.
			files = append(files, normentity.ChangedFile{Path: n.Path, Patch: normentity.Unknown[string](normentity.ReasonUnsupported)})
		}
		frag.ChangeProposal = normentity.Known(normentity.ChangeProposal{ChangedFiles: normentity.List[normentity.ChangedFile]{Known: files}})
	case entityacquire.NeedCommits:
		shas := make([]string, 0, len(nodes))
		for _, n := range nodes {
			if n.Commit == nil || n.Commit.OID == "" {
				return nil, errors.New("commit without an ID")
			}
			shas = append(shas, n.Commit.OID)
		}
		frag.ChangeProposal = normentity.Known(normentity.ChangeProposal{Commits: normentity.List[string]{Known: shas}})
	case entityacquire.NeedReactions:
		reactions := make([]normentity.Reaction, 0, len(nodes))
		for _, n := range nodes {
			if n.ID == "" {
				return nil, errors.New("reaction without an ID")
			}
			reactions = append(reactions, normentity.Reaction{
				ID:        n.ID,
				Content:   reactionContent(n.Content),
				Actor:     recordActor(n.User),
				CreatedAt: entityTimestamp(n.CreatedAt),
			})
		}
		frag.Reactions.Known = reactions
	case entityacquire.NeedLabelApplications:
		apps, err := a.labelApplications(nodes)
		if err != nil {
			return nil, err
		}
		frag.LabelApplications.Known = apps
	}
	return frag, nil
}

// labelApplications returns the most recent application of each currently
// applied label among one page of label events. Pages are read newest first
// and GitHub orders a page's events oldest first, so the page is walked in
// reverse: the first event seen for a label is its most recent one. A label
// whose most recent event removed it is not currently applied.
func (a *EntityAdapter) labelApplications(nodes []gqlEntityNode) ([]normentity.LabelApplication, error) {
	apps := make([]normentity.LabelApplication, 0, len(nodes))
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if n.Typename != "LabeledEvent" && n.Typename != "UnlabeledEvent" {
			return nil, fmt.Errorf("unexpected label event type %q", n.Typename)
		}
		if n.ID == "" || n.Label == nil || n.Label.Name == "" {
			// The event cannot be tied to a label, so the most recent
			// event of a current label may be unknown.
			return nil, errors.New("label event without an ID or label")
		}
		created, ok := entityTimestamp(n.CreatedAt).Value()
		if !ok {
			return nil, fmt.Errorf("label event %q without a valid time", n.ID)
		}
		name := n.Label.Name
		if a.labelEvents[name] {
			continue
		}
		a.labelEvents[name] = true
		if n.Typename == "UnlabeledEvent" {
			continue
		}
		apps = append(apps, normentity.LabelApplication{
			Action: normentity.Action{
				ID:        n.ID,
				CreatedAt: created,
				// A label application is never revised.
				RevisedAt: normentity.Known(created),
				Actor:     recordActor(n.Actor),
				// The entity author is not read with label events.
				IsEntityAuthor: normentity.Unknown[bool](normentity.ReasonNotLoaded),
			},
			Label: normentity.Known(name),
		})
	}
	return apps, nil
}

// reactionContents maps GitHub GraphQL reaction content to the REST names
// the normalized entity uses.
var reactionContents = map[string]string{
	"THUMBS_UP": "+1", "THUMBS_DOWN": "-1", "LAUGH": "laugh", "HOORAY": "hooray",
	"CONFUSED": "confused", "HEART": "heart", "ROCKET": "rocket", "EYES": "eyes",
}

func reactionContent(s string) normentity.Field[string] {
	if c, ok := reactionContents[s]; ok {
		return normentity.Known(c)
	}
	return normentity.Unknown[string](normentity.ReasonUnsupported)
}

// query runs one GraphQL query and decodes the response into out. A GitHub
// rate limit is reported as [entityacquire.ErrLimit].
func (a *EntityAdapter) query(ctx context.Context, q string, vars map[string]any, out any) error {
	resp, err := a.c.post(ctx, a.c.graphqlEndpoint(), map[string]any{"query": q, "variables": vars})
	if err != nil {
		if IsRateLimitError(err) {
			return fmt.Errorf("%w: %w", entityacquire.ErrLimit, err)
		}
		return err
	}
	var body struct {
		Errors []graphQLError `json:"errors"`
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if len(body.Errors) > 0 {
		e := &body.Errors[0]
		if e.Type == "RATE_LIMITED" {
			return fmt.Errorf("%w: %w", entityacquire.ErrLimit, e)
		}
		return e
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// entityRevision is the source revision of a read: the entity's updatedAt,
// and for a pull request also its head commit, which a push changes.
func entityRevision(kind normentity.Kind, updatedAt, headOID string) string {
	if updatedAt == "" || (kind == normentity.KindChangeProposal && headOID == "") {
		return ""
	}
	if kind == normentity.KindChangeProposal {
		return updatedAt + "@" + headOID
	}
	return updatedAt
}

func entityKind(typename string) (normentity.Kind, error) {
	switch typename {
	case "Issue":
		return normentity.KindWorkItem, nil
	case "PullRequest":
		return normentity.KindChangeProposal, nil
	}
	return "", fmt.Errorf("unsupported entity type %q", typename)
}

func entityState(kind normentity.Kind, s string) normentity.Field[normentity.State] {
	switch {
	case s == "OPEN":
		return normentity.Known(normentity.StateOpen)
	case s == "CLOSED":
		return normentity.Known(normentity.StateClosed)
	case s == "MERGED" && kind == normentity.KindChangeProposal:
		return normentity.Known(normentity.StateMerged)
	}
	return normentity.Unknown[normentity.State](normentity.ReasonUnsupported)
}

// entityTimestamp converts a GitHub timestamp to the normalized form.
func entityTimestamp(s string) normentity.Field[normentity.Timestamp] {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return normentity.Unknown[normentity.Timestamp](normentity.ReasonFailed)
	}
	return normentity.Known(normentity.Timestamp(t.UTC().Format("2006-01-02T15:04:05.000000000Z")))
}

// entityActor maps a GitHub actor to the shared actor shape. Roles are left
// unknown: the authorized entity view resolves them.
func entityActor(g *gqlEntityActor) normentity.Field[normentity.Actor] {
	if g == nil {
		// A deleted account or an actor GitHub does not report.
		return normentity.Unknown[normentity.Actor](normentity.ReasonUnsupported)
	}
	a := normentity.Actor{
		ID:           normentity.Unknown[string](normentity.ReasonUnsupported),
		Login:        normentity.Null[string](),
		Kind:         normentity.Unknown[normentity.ActorKind](normentity.ReasonUnsupported),
		Role:         normentity.Unknown[normentity.Role](normentity.ReasonNotLoaded),
		RoleVerified: normentity.Unknown[bool](normentity.ReasonNotLoaded),
	}
	if g.Login != "" {
		a.Login = normentity.Known(g.Login)
	}
	switch g.Typename {
	case "User":
		a.Kind = normentity.Known(normentity.ActorHuman)
	case "Bot":
		a.Kind = normentity.Known(normentity.ActorBot)
		a.Role = normentity.Known(normentity.RoleNone)
		a.BotRole = normentity.Unknown[string](normentity.ReasonNotLoaded)
	default:
		return normentity.Known(a)
	}
	if g.DatabaseID != nil && *g.DatabaseID > 0 {
		a.ID = normentity.Known(strconv.FormatInt(*g.DatabaseID, 10))
	}
	return normentity.Known(a)
}

// recordActor maps the actor of a reaction or action record, which always
// carries an actor. An actor GitHub does not report has every attribute
// unresolved, so the authorized entity view cannot admit it.
func recordActor(g *gqlEntityActor) normentity.Actor {
	if a, ok := entityActor(g).Value(); ok {
		return a
	}
	return normentity.Actor{
		ID:           normentity.Unknown[string](normentity.ReasonUnsupported),
		Login:        normentity.Unknown[string](normentity.ReasonUnsupported),
		Kind:         normentity.Unknown[normentity.ActorKind](normentity.ReasonUnsupported),
		Role:         normentity.Unknown[normentity.Role](normentity.ReasonNotLoaded),
		RoleVerified: normentity.Unknown[bool](normentity.ReasonNotLoaded),
	}
}

func entityLifecycle(kind normentity.Kind, state normentity.Field[normentity.State], g *gqlEntityBase) normentity.Lifecycle {
	var l normentity.Lifecycle
	s, ok := state.Value()
	switch {
	case !ok:
		l.ClosedAt = normentity.Unknown[normentity.Timestamp](normentity.ReasonUnsupported)
		l.ClosedBy = normentity.Unknown[normentity.Actor](normentity.ReasonUnsupported)
	case s == normentity.StateOpen:
		l.ClosedAt, l.ClosedBy = normentity.Null[normentity.Timestamp](), normentity.Null[normentity.Actor]()
	default:
		l.ClosedAt = normentity.Unknown[normentity.Timestamp](normentity.ReasonFailed)
		if g.ClosedAt != nil {
			l.ClosedAt = entityTimestamp(*g.ClosedAt)
		}
		// The closing actor needs a timeline read.
		l.ClosedBy = normentity.Unknown[normentity.Actor](normentity.ReasonUnsupported)
	}
	if kind != normentity.KindChangeProposal {
		return l
	}
	switch {
	case !ok:
		l.MergedAt = normentity.Unknown[normentity.Timestamp](normentity.ReasonUnsupported)
		l.MergedBy = normentity.Unknown[normentity.Actor](normentity.ReasonUnsupported)
	case s != normentity.StateMerged:
		l.MergedAt, l.MergedBy = normentity.Null[normentity.Timestamp](), normentity.Null[normentity.Actor]()
	default:
		l.MergedAt = normentity.Unknown[normentity.Timestamp](normentity.ReasonFailed)
		if g.MergedAt != nil {
			l.MergedAt = entityTimestamp(*g.MergedAt)
		}
		l.MergedBy = entityActor(g.MergedBy)
	}
	return l
}

func entityWorkItem(g *gqlEntityBase) normentity.Field[normentity.WorkItem] {
	w := normentity.WorkItem{
		SourceStatus: normentity.Known(normentity.SourceStatus{
			Name:     normentity.Known(g.State),
			Category: normentity.Unknown[string](normentity.ReasonUnsupported),
		}),
		IssueType:   normentity.Unknown[normentity.IssueType](normentity.ReasonUnsupported),
		StateReason: normentity.Null[string](),
	}
	if g.State == "" {
		w.SourceStatus = normentity.Unknown[normentity.SourceStatus](normentity.ReasonFailed)
	}
	if g.StateReason != nil && *g.StateReason != "" {
		w.StateReason = normentity.Known(strings.ToLower(*g.StateReason))
	}
	return normentity.Known(w)
}

func entityChangeProposal(g *gqlEntityBase) normentity.Field[normentity.ChangeProposal] {
	if g.BaseRepository == nil || g.BaseRepository.NameWithOwner == "" || g.BaseRefName == "" || g.HeadRefName == "" || g.HeadRefOid == "" {
		return normentity.Unknown[normentity.ChangeProposal](normentity.ReasonFailed)
	}
	cp := normentity.ChangeProposal{
		BaseRepository: normentity.Known(strings.ToLower(g.BaseRepository.NameWithOwner)),
		HeadRepository: normentity.Null[string](),
		BaseRef:        normentity.Known(g.BaseRefName),
		HeadRef:        normentity.Known(g.HeadRefName),
		HeadSHA:        normentity.Known(g.HeadRefOid),
		IsFork:         normentity.Known(g.IsCrossRepository),
		Draft:          normentity.Known(g.IsDraft),
		// The approval gate needs review decisions and branch rules.
		Approval: normentity.Unknown[normentity.Approval](normentity.ReasonUnsupported),
	}
	if g.HeadRepository != nil {
		cp.HeadRepository = normentity.Known(strings.ToLower(g.HeadRepository.NameWithOwner))
	}
	return normentity.Known(cp)
}

// safeRepoPath reports whether p is a clean repository-relative path.
func safeRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") {
		return false
	}
	if strings.ContainsFunc(p, unicode.IsControl) {
		return false
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}
