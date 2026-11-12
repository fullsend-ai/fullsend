package normentity

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// SchemaVersion is the `schema_version` of a serialized document.
const SchemaVersion = "normalized-entity/v1"

// ErrInvalid reports a document that violates an identity invariant of the
// normalized-entity v1 specification. See [Document.Validate].
var ErrInvalid = errors.New("normentity: invalid document")

// View is the host-only metadata describing how an entity was resolved. It is
// not visible to CEL.
type View struct {
	// HumanMinRole is the harness threshold applied to known actions.
	HumanMinRole Role `json:"human_min_role"`
	// ResolvedAt is the check time every known value is authoritative for.
	ResolvedAt Timestamp `json:"resolved_at"`
	// FilterVersion is the active content-safety pipeline version.
	FilterVersion string `json:"filter_version"`
}

// Document is a normalized-entity v1 document: the [Entity] a CEL binding
// exposes and the host-only [View].
//
// The specification's host-only `coverage` member has no counterpart here.
// Each collection's coverage lives in its [List], next to the known members it
// qualifies, because a CEL binding needs both together and keeping them in
// separate structures would require re-joining them for every evaluation.
// MarshalJSON gathers the coverage back into the serialized `coverage`
// member, and UnmarshalJSON rejoins it, so the serialized form is exactly the
// specified document.
//
// Decoding is not a JSON Schema validator: member names match
// case-insensitively and members outside the closed schema are dropped. Check
// untrusted documents against the schema first. [Document.Validate] checks
// the identity invariants the schema cannot express.
type Document struct {
	Entity Entity
	View   View
}

type wireDocument struct {
	SchemaVersion string       `json:"schema_version"`
	Entity        *Entity      `json:"entity"`
	Coverage      wireCoverage `json:"coverage"`
	View          View         `json:"view"`
}

// MarshalJSON implements [json.Marshaler].
func (d Document) MarshalJSON() ([]byte, error) {
	var c collector
	walkCoverage(&d.Entity, &c)
	w := wireDocument{
		SchemaVersion: SchemaVersion,
		Entity:        &d.Entity,
		Coverage:      c.w,
		View:          d.View,
	}
	// The specification requires both row arrays even when empty.
	if w.Coverage.ThreadComments == nil {
		w.Coverage.ThreadComments = []threadCommentCoverage{}
	}
	if w.Coverage.ExtensionLists == nil {
		w.Coverage.ExtensionLists = []extensionListCoverage{}
	}
	// Check rows accompany the checks collection, which only a change
	// proposal has.
	if !d.Entity.Checks.IsZero() {
		if w.Coverage.CheckLogs == nil {
			w.Coverage.CheckLogs = []checkLogCoverage{}
		}
		if w.Coverage.CheckAnnotations == nil {
			w.Coverage.CheckAnnotations = []checkAnnotationCoverage{}
		}
	}
	return json.Marshal(w)
}

// UnmarshalJSON implements [json.Unmarshaler].
func (d *Document) UnmarshalJSON(data []byte) error {
	var w struct {
		SchemaVersion string       `json:"schema_version"`
		Entity        Entity       `json:"entity"`
		Coverage      wireCoverage `json:"coverage"`
		View          View         `json:"view"`
	}
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	if w.SchemaVersion != SchemaVersion {
		return fmt.Errorf("normentity: schema_version %q is not %q", w.SchemaVersion, SchemaVersion)
	}
	a := applier{w: &w.Coverage, threads: map[string]*threadCommentCoverage{},
		logs: map[string]*checkLogCoverage{}, annotations: map[string]*checkAnnotationCoverage{},
		lists: map[extensionKey]*extensionListCoverage{}}
	for i := range w.Coverage.ThreadComments {
		r := &w.Coverage.ThreadComments[i]
		a.add(r.ThreadID, a.threads[r.ThreadID] != nil, "thread_comments")
		a.threads[r.ThreadID] = r
	}
	for i := range w.Coverage.CheckLogs {
		r := &w.Coverage.CheckLogs[i]
		a.add(r.CheckID, a.logs[r.CheckID] != nil, "check_logs")
		a.logs[r.CheckID] = r
	}
	for i := range w.Coverage.CheckAnnotations {
		r := &w.Coverage.CheckAnnotations[i]
		a.add(r.CheckID, a.annotations[r.CheckID] != nil, "check_annotations")
		a.annotations[r.CheckID] = r
	}
	for i := range w.Coverage.ExtensionLists {
		r := &w.Coverage.ExtensionLists[i]
		k := extensionKey{r.OwnerKind, r.OwnerID, r.Category, r.Alias}
		a.add(r.OwnerID+"/"+r.Alias, a.lists[k] != nil, "extension_lists")
		a.lists[k] = r
	}
	walkCoverage(&w.Entity, &a)
	if err := a.finish(); err != nil {
		return err
	}
	*d = Document{Entity: w.Entity, View: w.View}
	return nil
}

// Validate checks the identity invariants JSON Schema cannot express: the
// entity's kind and source system are specified values, the source host is
// lowercase, and `entity.id` is exactly the canonical ID derived from the
// source system, host, kind, and source ID. It returns [ErrInvalid] wrapped
// with the first violation.
func (d *Document) Validate() error {
	e := &d.Entity
	switch e.Kind {
	case KindWorkItem, KindChangeProposal, KindConversation:
	default:
		return fmt.Errorf("%w: unknown entity kind %q", ErrInvalid, e.Kind)
	}
	switch e.Source.System {
	case SystemGitHub, SystemGitLab, SystemJira:
	default:
		return fmt.Errorf("%w: unknown source system %q", ErrInvalid, e.Source.System)
	}
	if e.Source.Host != strings.ToLower(e.Source.Host) {
		return fmt.Errorf("%w: source host %q is not lowercase", ErrInvalid, e.Source.Host)
	}
	want, err := NewID(string(e.Source.System), e.Source.Host, string(e.Kind), e.SourceID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if e.ID != want {
		return fmt.Errorf("%w: entity.id %q is not the canonical ID %q of its source identity", ErrInvalid, e.ID, want)
	}
	return nil
}

// wireCoverage is the serialized host-only `coverage` member.
type wireCoverage struct {
	Labels            *Coverage `json:"labels,omitempty"`
	Comments          *Coverage `json:"comments,omitempty"`
	LabelApplications *Coverage `json:"label_applications,omitempty"`
	Reviews           *Coverage `json:"reviews,omitempty"`
	Checks            *Coverage `json:"checks,omitempty"`
	Threads           *Coverage `json:"threads,omitempty"`
	Reviewers         *Coverage `json:"reviewers,omitempty"`
	CurrentApprovals  *Coverage `json:"current_approvals,omitempty"`
	Assignees         *Coverage `json:"assignees,omitempty"`
	Reactions         *Coverage `json:"reactions,omitempty"`
	ChangedFiles      *Coverage `json:"changed_files,omitempty"`
	Commits           *Coverage `json:"commits,omitempty"`

	ThreadComments   []threadCommentCoverage   `json:"thread_comments"`
	CheckLogs        []checkLogCoverage        `json:"check_logs,omitzero"`
	CheckAnnotations []checkAnnotationCoverage `json:"check_annotations,omitzero"`
	ExtensionLists   []extensionListCoverage   `json:"extension_lists"`
}

type threadCommentCoverage struct {
	ThreadID string `json:"thread_id"`
	Coverage
}

type checkLogCoverage struct {
	CheckID    string   `json:"check_id"`
	Ascending  Coverage `json:"ascending"`
	Descending Coverage `json:"descending"`
}

type checkAnnotationCoverage struct {
	CheckID  string   `json:"check_id"`
	Coverage Coverage `json:"coverage"`
}

type extensionListCoverage struct {
	OwnerKind string   `json:"owner_kind"`
	OwnerID   string   `json:"owner_id"`
	Category  string   `json:"category"`
	Alias     string   `json:"alias"`
	Coverage  Coverage `json:"coverage"`
}

type extensionKey struct{ ownerKind, ownerID, category, alias string }

// slot returns the named entity-level coverage member.
func (w *wireCoverage) slot(name string) **Coverage {
	switch name {
	case "labels":
		return &w.Labels
	case "comments":
		return &w.Comments
	case "label_applications":
		return &w.LabelApplications
	case "reviews":
		return &w.Reviews
	case "checks":
		return &w.Checks
	case "threads":
		return &w.Threads
	case "reviewers":
		return &w.Reviewers
	case "current_approvals":
		return &w.CurrentApprovals
	case "assignees":
		return &w.Assignees
	case "reactions":
		return &w.Reactions
	case "changed_files":
		return &w.ChangedFiles
	}
	return &w.Commits
}

// coverageSink receives a pointer to every [Coverage] held by a collection in
// an entity. A single walk shared by marshaling and unmarshaling guarantees
// the two directions agree on where each coverage entry belongs.
type coverageSink interface {
	// entity receives a collection coverage entry named as in the
	// specification's `coverage` member; present reports whether the
	// collection itself is present.
	entity(name string, present bool, c *Coverage)
	thread(id string, c *Coverage)
	checkLog(id string, ascending, descending *Coverage)
	checkAnnotations(id string, c *Coverage)
	extensionList(k extensionKey, c *Coverage)
}

func walkCoverage(e *Entity, s coverageSink) {
	list := func(name string, present bool, c *Coverage) { s.entity(name, present, c) }
	list("labels", !e.Labels.IsZero(), &e.Labels.Coverage)
	list("assignees", !e.Assignees.IsZero(), &e.Assignees.Coverage)
	list("comments", !e.Comments.IsZero(), &e.Comments.Coverage)
	list("reviews", !e.Reviews.IsZero(), &e.Reviews.Coverage)
	list("reviewers", !e.Reviewers.IsZero(), &e.Reviewers.Coverage)
	list("current_approvals", !e.CurrentApprovals.IsZero(), &e.CurrentApprovals.Coverage)
	list("label_applications", !e.LabelApplications.IsZero(), &e.LabelApplications.Coverage)
	list("threads", !e.Threads.IsZero(), &e.Threads.Coverage)
	list("reactions", !e.Reactions.IsZero(), &e.Reactions.Coverage)
	list("checks", !e.Checks.IsZero(), &e.Checks.Coverage)
	if cp := e.ChangeProposal.Ptr(); cp != nil {
		list("changed_files", !cp.ChangedFiles.IsZero(), &cp.ChangedFiles.Coverage)
		list("commits", !cp.Commits.IsZero(), &cp.Commits.Coverage)
	} else {
		// An unresolved change proposal still has both coverage entries.
		u := &e.UnresolvedChangeProposal
		present := !e.ChangeProposal.IsZero()
		list("changed_files", present, &u.ChangedFiles)
		list("commits", present, &u.Commits)
	}

	walkExtensions("entity", e.SourceID, e.Extensions, s)
	if b := e.Body.Ptr(); b != nil {
		walkExtensions("body", b.ID, b.Extensions, s)
	}
	for i := range e.Comments.Known {
		c := &e.Comments.Known[i]
		walkExtensions("comment", c.ID, c.Extensions, s)
	}
	for i := range e.Reviews.Known {
		r := &e.Reviews.Known[i]
		walkExtensions("review", r.ID, r.Extensions, s)
	}
	for i := range e.LabelApplications.Known {
		l := &e.LabelApplications.Known[i]
		walkExtensions("label_application", l.ID, l.Extensions, s)
	}
	for i := range e.Threads.Known {
		t := &e.Threads.Known[i]
		s.thread(t.ID, &t.CommentIDs.Coverage)
		walkExtensions("thread", t.ID, t.Extensions, s)
	}
	for i := range e.Checks.Known {
		c := &e.Checks.Known[i]
		s.checkLog(c.ID, &c.Log.Ascending.Coverage, &c.Log.Descending.Coverage)
		s.checkAnnotations(c.ID, &c.Report.Annotations.Coverage)
		walkExtensions("check", c.ID, c.Extensions, s)
	}
}

// walkExtensions visits the coverage of every list-valued extension of one
// owning record, in a deterministic order.
func walkExtensions(ownerKind, ownerID string, x *Extensions, s coverageSink) {
	if x == nil {
		return
	}
	for _, cat := range []struct {
		name    string
		entries map[string]ExtensionEntry
	}{{"custom_fields", x.CustomFields}, {"properties", x.Properties}} {
		for _, alias := range slices.Sorted(maps.Keys(cat.entries)) {
			entry := cat.entries[alias]
			if v := entry.Value.Ptr(); v != nil && v.List != nil {
				s.extensionList(extensionKey{ownerKind, ownerID, cat.name, alias}, &v.List.Coverage)
			}
		}
	}
}

// collector gathers every recorded coverage into the serialized member.
type collector struct{ w wireCoverage }

func (c *collector) entity(name string, _ bool, cov *Coverage) {
	if !cov.IsZero() {
		cp := *cov
		*c.w.slot(name) = &cp
	}
}

func (c *collector) thread(id string, cov *Coverage) {
	if !cov.IsZero() {
		c.w.ThreadComments = append(c.w.ThreadComments, threadCommentCoverage{ThreadID: id, Coverage: *cov})
	}
}

func (c *collector) checkLog(id string, asc, desc *Coverage) {
	if !asc.IsZero() || !desc.IsZero() {
		c.w.CheckLogs = append(c.w.CheckLogs, checkLogCoverage{CheckID: id, Ascending: *asc, Descending: *desc})
	}
}

func (c *collector) checkAnnotations(id string, cov *Coverage) {
	if !cov.IsZero() {
		c.w.CheckAnnotations = append(c.w.CheckAnnotations, checkAnnotationCoverage{CheckID: id, Coverage: *cov})
	}
}

func (c *collector) extensionList(k extensionKey, cov *Coverage) {
	if !cov.IsZero() {
		c.w.ExtensionLists = append(c.w.ExtensionLists, extensionListCoverage{
			OwnerKind: k.ownerKind, OwnerID: k.ownerID, Category: k.category, Alias: k.alias, Coverage: *cov})
	}
}

// applier rejoins serialized coverage onto the collections it describes and
// records any entry that describes no collection or is duplicated.
type applier struct {
	w           *wireCoverage
	threads     map[string]*threadCommentCoverage
	logs        map[string]*checkLogCoverage
	annotations map[string]*checkAnnotationCoverage
	lists       map[extensionKey]*extensionListCoverage
	errs        []error
}

func (a *applier) add(id string, duplicate bool, member string) {
	if duplicate {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.%s has more than one entry for %q", member, id))
	}
}

func (a *applier) entity(name string, present bool, c *Coverage) {
	slot := a.w.slot(name)
	if *slot == nil {
		return
	}
	if !present {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.%s describes a collection the entity does not have", name))
	}
	*c = **slot
	*slot = nil
}

func (a *applier) thread(id string, c *Coverage) {
	if r, ok := a.threads[id]; ok {
		*c = r.Coverage
		delete(a.threads, id)
	}
}

func (a *applier) checkLog(id string, asc, desc *Coverage) {
	if r, ok := a.logs[id]; ok {
		*asc, *desc = r.Ascending, r.Descending
		delete(a.logs, id)
	}
}

func (a *applier) checkAnnotations(id string, c *Coverage) {
	if r, ok := a.annotations[id]; ok {
		*c = r.Coverage
		delete(a.annotations, id)
	}
}

func (a *applier) extensionList(k extensionKey, c *Coverage) {
	if r, ok := a.lists[k]; ok {
		*c = r.Coverage
		delete(a.lists, k)
	}
}

// finish reports coverage entries that matched no collection.
func (a *applier) finish() error {
	for id := range a.threads {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.thread_comments entry %q matches no thread", id))
	}
	for id := range a.logs {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.check_logs entry %q matches no check", id))
	}
	for id := range a.annotations {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.check_annotations entry %q matches no check", id))
	}
	for k := range a.lists {
		a.errs = append(a.errs, fmt.Errorf("normentity: coverage.extension_lists entry %q matches no list-valued extension", k.ownerID+"/"+k.alias))
	}
	return errors.Join(a.errs...)
}
