package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/internal/urlutil"
)

// Generic, tracker-independent work-item identity variables (ADR 0063).
// Harnesses and their scripts read these instead of forge-specific
// variables such as ISSUE_NUMBER, which only identify GitHub/GitLab issues.
const (
	envWorkItemSource = "FULLSEND_WORK_ITEM_SOURCE"
	envWorkItemKey    = "FULLSEND_WORK_ITEM_KEY"
	envWorkItemURL    = "FULLSEND_WORK_ITEM_URL"
)

// jiraWorkItemKeyPattern matches a Jira issue key such as "PROJ-123": an
// uppercase project key followed by a positive issue number. It is stricter
// than parseJiraKey so a key exported to harness scripts cannot carry
// whitespace, shell metacharacters, or path separators.
var jiraWorkItemKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]+-[1-9][0-9]*$`)

// resolveWorkItemIdentity returns the FULLSEND_WORK_ITEM_* variables that
// run exports before harness env expansion.
//
// For a Jira-sourced event the normalized event is authoritative: the
// source, the textual key (PROJ-123), and the browse URL all come from it
// and override any inherited values. A missing or malformed key or URL is
// an error so the run fails closed instead of guessing an identity.
//
// For forge-sourced events (and runs with no normalized event) only
// FULLSEND_WORK_ITEM_SOURCE is filled in, and only when the caller did not
// already set it. FULLSEND_WORK_ITEM_KEY and FULLSEND_WORK_ITEM_URL keep the
// values the dispatch workflow exported, so GitHub/GitLab behavior is
// unchanged.
func resolveWorkItemIdentity(eventMap map[string]any, trackerSource, forgePlatform string, getenv func(string) string) (map[string]string, error) {
	if trackerSource == string(normevent.SystemJira) {
		key := extractMapString(eventMap, "entity", "key")
		if !jiraWorkItemKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("Jira work item has missing or malformed entity.key %q (want a key like PROJ-123)", key)
		}
		rawURL := extractMapString(eventMap, "entity", "url")
		if !urlutil.IsURL(rawURL) {
			return nil, fmt.Errorf("Jira work item %s has missing or malformed entity.url %q (want an https URL)", key, rawURL)
		}
		return map[string]string{
			envWorkItemSource: string(normevent.SystemJira),
			envWorkItemKey:    key,
			envWorkItemURL:    rawURL,
		}, nil
	}

	if getenv(envWorkItemSource) != "" {
		return nil, nil
	}
	source := trackerSource
	if source == "" {
		source = forgePlatform
	}
	if source == "" {
		return nil, nil
	}
	return map[string]string{envWorkItemSource: source}, nil
}

// issueNumberFlagEnv returns the ISSUE_NUMBER value run exports and whether
// to export it at all.
//
// A Jira work item has no forge issue number. For Jira runs statusNum holds
// the numeric suffix of the Jira key (used only for Jira status routing),
// and an inherited ISSUE_NUMBER may carry the numeric Jira entity ID, so
// ISSUE_NUMBER is cleared rather than letting scripts treat either as a
// GitHub/GitLab issue number. Scripts identify the work item through
// FULLSEND_WORK_ITEM_KEY instead. Forge runs keep exporting --status-number.
func issueNumberFlagEnv(sOpts statusOpts) (string, bool) {
	if sOpts.trackerSource == string(normevent.SystemJira) {
		return "", true
	}
	if sOpts.statusNum != 0 {
		return strconv.Itoa(sOpts.statusNum), true
	}
	return "", false
}

// dispatchEventPayloads returns the raw dispatch event payloads that run
// can read a normalized event from, in priority order: the on-disk
// dispatch/event-payload.json written by reusable-dispatch.yml, then the
// inputs.event_payload of the workflow_call event at GITHUB_EVENT_PATH.
func dispatchEventPayloads(fullsendDir string) [][]byte {
	var payloads [][]byte
	if data, err := os.ReadFile(filepath.Join(fullsendDir, "dispatch", "event-payload.json")); err == nil {
		payloads = append(payloads, data)
	}
	ghEventPath := os.Getenv("GITHUB_EVENT_PATH")
	if ghEventPath == "" {
		return payloads
	}
	data, err := os.ReadFile(ghEventPath)
	if err != nil {
		return payloads
	}
	var wrapper struct {
		Inputs struct {
			EventPayload string `json:"event_payload"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil || wrapper.Inputs.EventPayload == "" {
		return payloads
	}
	return append(payloads, []byte(wrapper.Inputs.EventPayload))
}

// dispatchJiraClaimUnresolved reports whether run must fail closed because a
// dispatch payload claims a Jira source that was not resolved into a valid
// Jira work-item identity. It applies only when no --event-file was given and
// trackerSource is not already "jira" — including when trackerSource is a
// forge value taken from the other dispatch channel.
func dispatchJiraClaimUnresolved(eventFile, trackerSource, fullsendDir string) bool {
	if eventFile != "" || trackerSource == string(normevent.SystemJira) {
		return false
	}
	return dispatchPayloadClaimsJira(fullsendDir)
}

// dispatchPayloadClaimsJira reports whether any dispatch event payload
// embeds a _normalized_event whose source.system is "jira", without
// validating the rest of the event. run uses it to fail closed when a
// Jira-sourced payload did not pass normalized-event validation: falling
// back to forge routing would treat the numeric Jira entity ID as a forge
// issue number.
func dispatchPayloadClaimsJira(fullsendDir string) bool {
	for _, payload := range dispatchEventPayloads(fullsendDir) {
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err != nil {
			continue
		}
		norm, ok := raw["_normalized_event"].(map[string]any)
		if !ok {
			continue
		}
		if extractMapString(norm, "source", "system") == string(normevent.SystemJira) {
			return true
		}
	}
	return false
}
