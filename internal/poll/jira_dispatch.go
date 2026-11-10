package poll

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"regexp"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
	"gopkg.in/yaml.v3"
)

// JiraEventTypePrefix prefixes the EVENT_TYPE of a Jira work-item dispatch
// (#8919), for example "jira_comment". EVENT_TYPE is part of the signed
// dispatch message, so run-agent-job.sh can trust the prefix to select its
// Jira handling once the HMAC has been verified.
const JiraEventTypePrefix = "jira_"

// jiraResourceKeyPrefix prefixes the RESOURCE_KEY of a Jira work-item
// dispatch ("jira-PROJ-123"). Keep in sync with the resource_key input
// regex in the GitLab scaffold (.gitlab-ci.yml, fullsend-pipeline.yml,
// fullsend-agent.yml).
const jiraResourceKeyPrefix = "jira-"

// jiraIssueKeyPattern matches a Jira issue key such as "PROJ-123". It is the
// same pattern fullsend run enforces before exporting FULLSEND_WORK_ITEM_KEY
// (jiraWorkItemKeyPattern in internal/cli/run_workitem.go), so a key that
// reaches the agent pipeline is also one the run accepts.
var jiraIssueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-[1-9][0-9]*$`)

// dispatchStagePattern matches the stage pipeline input regex the GitLab
// scaffold declares.
var dispatchStagePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// jiraRawTypePattern bounds the normalized source.raw_type folded into
// EVENT_TYPE to plain lowercase identifiers.
var jiraRawTypePattern = regexp.MustCompile(`^[a-z][a-z_]*$`)

// jiraCommentLimits are the successive rune limits DispatchJiraEvent tries
// for the comment body and instruction until the payload fits the
// event_payload_chunk_NN budget (see maxEventPayloadChunks). The first
// matches noteBodyTransportLimit, buildEventPayload's bound for
// non-command GitLab note bodies.
var jiraCommentLimits = []int{800, 400, 200, 100, 0}

// DispatchJiraEvent starts the protected agent pipeline for stage with a
// Jira work-item event (#8919). It reuses the GitLab dispatch route of
// dispatch(): the same typed pipeline inputs, FULLSEND_DISPATCH_SECRET HMAC,
// typed-contract check of the committed wrapper at p.opts.PipelineRef, and
// the Poller API or CI_JOB_TOKEN child-pipeline route. run-agent-job.sh
// then applies the usual identity pin, creator check, HMAC verification,
// protected-ref check, and role credential selection.
//
// The event must be a Jira work item that targets this poller's project,
// with a valid Jira key and an https browse URL. Anything else, a missing
// dispatch secret, or a committed wrapper that cannot accept the Jira
// resource key fails closed before a pipeline is created.
//
// The dispatch carries no ACTOR_ID or STATUS_IID: a Jira actor has no
// GitLab user ID, and a Jira issue has no GitLab IID. Authorization was
// already decided by the Jira poller from the actor's Jira project role.
func (p *Poller) DispatchJiraEvent(ctx context.Context, stage string, event *normevent.Event) error {
	if event == nil {
		return fmt.Errorf("jira dispatch: event is required")
	}
	if !dispatchStagePattern.MatchString(stage) {
		return fmt.Errorf("jira dispatch: stage %q is not a valid agent name", stage)
	}
	if event.Source.System != normevent.SystemJira {
		return fmt.Errorf("jira dispatch: event source %q is not jira", event.Source.System)
	}
	if event.Entity.Kind != normevent.EntityWorkItem {
		return fmt.Errorf("jira dispatch: entity kind %q is not a work item", event.Entity.Kind)
	}
	if event.Repo != p.projectPath {
		return fmt.Errorf("jira dispatch: event targets %q, not the GitLab project %q", event.Repo, p.projectPath)
	}
	key := event.Entity.Key
	if !jiraIssueKeyPattern.MatchString(key) {
		return fmt.Errorf("jira dispatch: malformed Jira key %q", key)
	}
	if !urlutil.IsURL(event.Entity.URL) {
		return fmt.Errorf("jira dispatch: %s has a missing or non-https URL %q", key, event.Entity.URL)
	}
	if !jiraRawTypePattern.MatchString(event.Source.RawType) {
		return fmt.Errorf("jira dispatch: %s has an unsupported source.raw_type %q", key, event.Source.RawType)
	}
	if p.opts.DispatchSecret == "" {
		return fmt.Errorf("jira dispatch: FULLSEND_DISPATCH_SECRET is required to sign Jira dispatches")
	}

	eventType := JiraEventTypePrefix + event.Source.RawType
	rk := jiraResourceKeyPrefix + key
	encoded, err := encodeJiraEventPayload(eventType, event)
	if err != nil {
		return fmt.Errorf("jira dispatch for %s/%s: %w", stage, rk, err)
	}

	variables := map[string]string{
		"STAGE":             stage,
		"EVENT_TYPE":        eventType,
		"EVENT_PAYLOAD_B64": encoded,
		"RESOURCE_KEY":      rk,
		"IS_FORK":           "false",
		"ORIGINATING_URL":   event.Entity.URL,
		"REPO_FULL_NAME":    p.projectPath,
	}
	if p.opts.PollJobURL != "" {
		variables[forge.VarPollJobURL] = p.opts.PollJobURL
	}
	variables[forge.VarDispatchHMAC] = computeDispatchHMAC(p.opts.DispatchSecret, variables)

	if err := p.checkJiraDispatchContract(ctx, rk); err != nil {
		return fmt.Errorf("jira dispatch for %s/%s: %w", stage, rk, err)
	}

	webURL, err := p.createAgentPipeline(ctx, p.owner, p.repo, stage, rk, variables)
	if err != nil {
		return err
	}
	log.Printf("  → %s (%s %s, %s)", webURL, eventType, key, stage)
	return nil
}

// checkJiraDispatchContract confirms the committed pipeline wrapper at the
// dispatch ref declares the typed pipeline-input contract and that its
// resource_key input accepts rk. A wrapper from a release before Jira
// dispatch constrains resource_key to issue and MR keys, so GitLab would
// reject the pipeline; failing here names the fix instead.
func (p *Poller) checkJiraDispatchContract(ctx context.Context, rk string) error {
	content, err := p.client.GetFileContentAtRef(ctx, p.owner, p.repo, fullsendPipelineIncludePath, p.opts.PipelineRef)
	if err != nil && !forge.IsNotFound(err) {
		return fmt.Errorf("checking GitLab dispatch transport: %w", err)
	}
	if err != nil || !gitlabWrapperDeclaresTypedDispatch(content) {
		return fmt.Errorf("the project has no typed pipeline-input contract in %s at ref %q; install fullsend in the project with repos install", fullsendPipelineIncludePath, p.opts.PipelineRef)
	}
	if !gitlabWrapperAcceptsResourceKey(content, rk) {
		return fmt.Errorf("the resource_key input in %s at ref %q does not accept Jira work items; rerun repos install to update the scaffold", fullsendPipelineIncludePath, p.opts.PipelineRef)
	}
	return nil
}

// gitlabWrapperAcceptsResourceKey reports whether the resource_key input in
// content's spec:inputs header accepts rk. An input without a regex places
// no constraint on the value. GitLab evaluates input regexes as RE2, the
// syntax Go's regexp package implements; a regex that does not compile is
// treated as not accepting the key.
func gitlabWrapperAcceptsResourceKey(content []byte, rk string) bool {
	var header struct {
		Spec struct {
			Inputs map[string]struct {
				Regex string `yaml:"regex"`
			} `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := yaml.NewDecoder(bytes.NewReader(content)).Decode(&header); err != nil {
		return false
	}
	input, ok := header.Spec.Inputs[dispatchInputNames["RESOURCE_KEY"]]
	if !ok {
		return false
	}
	if input.Regex == "" {
		return true
	}
	re, err := regexp.Compile(input.Regex)
	if err != nil {
		return false
	}
	return re.MatchString(rk)
}

// encodeJiraEventPayload builds the base64 EVENT_PAYLOAD_B64 for a Jira
// dispatch: the event type and the normalized event, which run-agent-job.sh
// hands to fullsend run as its --event-file. The comment body and
// instruction are shortened, with noteBodyTruncationNotice appended, until
// the payload fits the maxEventPayloadChunks transport budget. It returns
// an error when the event does not fit even without comment text.
func encodeJiraEventPayload(eventType string, event *normevent.Event) (string, error) {
	budget := maxEventPayloadChunks * dispatchPayloadChunkSize
	for _, limit := range jiraCommentLimits {
		ev := *event
		if event.Transition.Comment != nil {
			c := *event.Transition.Comment
			c.Body = truncateNoteBody(c.Body, limit)
			c.Instruction = truncateNoteBody(c.Instruction, limit)
			ev.Transition.Comment = &c
		}
		normMap, err := ev.ToMap()
		if err != nil {
			return "", fmt.Errorf("converting normalized event: %w", err)
		}
		payload, err := json.Marshal(map[string]any{
			"type":              eventType,
			"_normalized_event": normMap,
		})
		if err != nil {
			return "", fmt.Errorf("marshaling event payload: %w", err)
		}
		encoded := base64.StdEncoding.EncodeToString(payload)
		if len(encoded) <= budget {
			return encoded, nil
		}
		if event.Transition.Comment == nil {
			break
		}
	}
	return "", fmt.Errorf("event payload exceeds the %d-character dispatch budget", budget)
}
