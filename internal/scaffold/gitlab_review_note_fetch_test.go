package scaffold

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The GitLab agent job fetches the review note in two places: the review
// stage's prior-review fetch (which anchors a re-review on the previous
// review) and the fix stage's /fs-fix review-body pre-fetch. Both paginate
// MR notes with sort=desc and select the first note of the review bot whose
// body carries the review marker — the newest one. Issue #9478 (deferred
// from PR 9399's review): nothing in this repo tested that choice for
// GitLab; pre-fetch-prior-review-test.sh covers only the GitHub fetcher,
// and not its newest-wins choice.
// Now that every re-review adds a marked note, newest-wins is load-bearing:
// an oldest-wins regression would anchor each re-review on the first review
// ever written. These tests run the shipped script sections under bash
// against a mocked fullsend_gate_curl, mirroring that script's cases.
const gitlabReviewMarker = "<!-- fullsend:review-agent -->"

// Fixture identities for the notes API mock: the review bot the fetchers
// filter by, and a bystander user whose marked notes must never win.
const (
	gitlabReviewBotID = 7
	gitlabOtherUserID = 9
)

// gitlabMRNote is one MR note as the GitLab notes API answers it.
type gitlabMRNote struct {
	ID     int `json:"id"`
	Author struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
	Body string `json:"body"`
}

func gitlabBotNote(id int, body string) gitlabMRNote {
	n := gitlabMRNote{ID: id, Body: body}
	n.Author.ID = gitlabReviewBotID
	n.Author.Username = "fullsend-review-bot"
	return n
}

func gitlabOtherUserNote(id int, body string) gitlabMRNote {
	n := gitlabMRNote{ID: id, Body: body}
	n.Author.ID = gitlabOtherUserID
	n.Author.Username = "some-human"
	return n
}

// gitlabMarkedReviewBody returns a review-note body carrying the marker both
// fetchers select on.
func gitlabMarkedReviewBody(text string) string {
	return gitlabReviewMarker + "\n" + text
}

// gitlabNotesJSON marshals notes as the API answers
// merge_requests/<iid>/notes?sort=desc&per_page=100: newest first, so the
// caller lists them newest-first.
func gitlabNotesJSON(t *testing.T, notes ...gitlabMRNote) string {
	t.Helper()
	raw, err := json.Marshal(notes)
	require.NoError(t, err)
	return string(raw)
}

// Section anchors in the shipped run-agent-job.sh. Both fetchers carry the
// same pagination loop and the same review-note branch opening, so tests
// locate their copy by occurrence index.
const (
	gitlabNotesLoopStart      = "    # Paginate through MR notes (newest first) to find the review marker."
	gitlabNotesLoopEnd        = "    done"
	gitlabReviewNoteBranch    = `    if [ -n "${REVIEW_NOTE}" ] && [ "${REVIEW_NOTE}" != "null" ]; then`
	gitlabReviewNoteBranchEnd = "    fi"
)

// runAgentJobSectionOccurrences returns every occurrence, in shipped order,
// of the run-agent-job.sh section that runs from the line starting with
// start through the first following line equal to end.
func runAgentJobSectionOccurrences(t *testing.T, script, start, end string) []string {
	t.Helper()
	var sections []string
	for rest := script; ; {
		i := strings.Index(rest, "\n"+start)
		if i < 0 {
			break
		}
		rest = rest[i+1:]
		j := strings.Index(rest, "\n"+end+"\n")
		require.GreaterOrEqual(t, j, 0, "section end %q not found after %q", end, start)
		sections = append(sections, rest[:j+1+len(end)+1])
		rest = rest[j+1+len(end)+1:]
	}
	require.NotEmpty(t, sections, "section start %q not found in run-agent-job.sh", start)
	return sections
}

// gitlabNotesFetcherSections holds one MR-notes fetcher's shipped sections:
// its pagination loop and its review-note branch.
type gitlabNotesFetcherSections struct {
	loop   string
	branch string
}

// gitlabNotesFetcherSectionsFor returns the shipped sections of both MR-notes
// fetchers: index 0 the review stage's prior-review fetch, index 1 the fix
// stage's /fs-fix review-body fetch (their shipped order). Exactly two of
// each must ship — a removed, added or reordered copy is a drift these tests
// must see.
func gitlabNotesFetcherSectionsFor(t *testing.T) [2]gitlabNotesFetcherSections {
	t.Helper()
	content, err := GitLabPerRepoFile(gitlabRunAgentJobScriptPath)
	require.NoError(t, err)
	script := string(content)
	loops := runAgentJobSectionOccurrences(t, script, gitlabNotesLoopStart, gitlabNotesLoopEnd)
	branches := runAgentJobSectionOccurrences(t, script, gitlabReviewNoteBranch, gitlabReviewNoteBranchEnd)
	require.Len(t, loops, 2, "run-agent-job.sh ships the MR-notes pagination loop twice: prior-review and /fs-fix review body")
	require.Len(t, branches, 2, "run-agent-job.sh ships the review-note branch twice: prior-review and /fs-fix review body")
	return [2]gitlabNotesFetcherSections{{loop: loops[0], branch: branches[0]}, {loop: loops[1], branch: branches[1]}}
}

// runGitlabNotesFetcherBash runs snippet under bash with fullsend_gate_curl
// mocked (the snippet defines it to cat a fixture file only for a notes URL
// carrying sort=desc, and to fail otherwise), PATH, OUT_DIR and
// NOTES_JSON_FILE set, and returns OUT_DIR and the combined output.
func runGitlabNotesFetcherBash(t *testing.T, snippet, notesJSON string) (string, string) {
	t.Helper()
	outDir := t.TempDir()
	notesFile := filepath.Join(outDir, "notes.json")
	require.NoError(t, os.WriteFile(notesFile, []byte(notesJSON), 0o644))
	cmd := exec.Command("bash", "-c", snippet)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"OUT_DIR=" + outDir,
		"NOTES_JSON_FILE=" + notesFile,
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "fetcher snippet output: %s", out)
	return outDir, string(out)
}

func readOutFile(t *testing.T, outDir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(outDir, name))
	require.NoError(t, err)
	return string(raw)
}

// gitlabNotesFetcherPreamble is the environment both fetchers' shipped
// sections run in: the mocked API endpoint and token (the mock serves only
// notes URLs carrying sort=desc), MR !3, and the review
// bot's user id. fetcherFile is the output file the shipped branch writes
// (the shipped code mktemps it before the section; here it is a file under
// OUT_DIR, written the same way). Both output variables point at it because
// each fetcher's branch writes only its own.
func gitlabNotesFetcherPreamble(fetcherFile string) []string {
	return []string{
		"set -euo pipefail",
		`fullsend_gate_curl() { case "$1" in *"sort=desc"*) cat "${NOTES_JSON_FILE}" ;; *) echo "unexpected notes URL: $1" >&2 ; return 1 ;; esac; }`,
		fmt.Sprintf(`BOT_ID="%d"`, gitlabReviewBotID),
		`MR_IID="3"`,
		`FULLSEND_PINNED_API_V4_URL="https://gitlab.example/api/v4"`,
		`FULLSEND_PINNED_PROJECT_ID="42"`,
		`FULLSEND_JOB_TOKEN="job-token"`,
		`REVIEW_BODY_FILE="${OUT_DIR}/` + fetcherFile + `"`,
		`PRIOR_REVIEW_FILE="${OUT_DIR}/` + fetcherFile + `"`,
		`: > "${OUT_DIR}/` + fetcherFile + `"`,
		`PRIOR_REVIEW_SHA=""`,
		`PRIOR_REVIEW_PROVENANCE="none"`,
	}
}

// runPriorReviewFetch runs the shipped prior-review fetcher (occurrence 0)
// over notesJSON and returns the anchored body, the provenance and the
// snippet's output. The body file carries jq's trailing newline.
func runPriorReviewFetch(t *testing.T, notesJSON string) (body, provenance, output string) {
	t.Helper()
	sections := gitlabNotesFetcherSectionsFor(t)[0]
	lines := append(gitlabNotesFetcherPreamble("prior-review-body"),
		sections.loop, sections.branch,
		`printf '%s' "${PRIOR_REVIEW_PROVENANCE-UNSET}" > "${OUT_DIR}/provenance"`)
	outDir, out := runGitlabNotesFetcherBash(t, strings.Join(lines, "\n"), notesJSON)
	return readOutFile(t, outDir, "prior-review-body"), readOutFile(t, outDir, "provenance"), out
}

// runFixReviewBodyFetch runs the shipped /fs-fix review-body fetcher
// (occurrence 1) over notesJSON and returns the review body it extracted
// (jq's trailing newline included) and the snippet's output.
func runFixReviewBodyFetch(t *testing.T, notesJSON string) (body, output string) {
	t.Helper()
	sections := gitlabNotesFetcherSectionsFor(t)[1]
	lines := append(gitlabNotesFetcherPreamble("review-body"), sections.loop, sections.branch)
	outDir, out := runGitlabNotesFetcherBash(t, strings.Join(lines, "\n"), notesJSON)
	return readOutFile(t, outDir, "review-body"), out
}

// The prior-review fetcher picks the newer of two review-bot notes that both
// carry the marker (#9478 ac_1): the first eligible note under sort=desc.
func TestRunAgentJobScript_PriorReviewFetchPicksNewestMarkedBotNote(t *testing.T) {
	notes := gitlabNotesJSON(t,
		gitlabBotNote(20, gitlabMarkedReviewBody("second review - must win")),
		gitlabBotNote(10, gitlabMarkedReviewBody("first review - must not win")),
	)

	body, provenance, _ := runPriorReviewFetch(t, notes)

	assert.Equal(t, gitlabMarkedReviewBody("second review - must win")+"\n", body)
	assert.Equal(t, "bot-verified", provenance)
}

// The /fs-fix review-body fetcher picks the newer of two marked review-bot
// notes too (#9478 ac_2).
func TestRunAgentJobScript_FixReviewBodyFetchPicksNewestMarkedBotNote(t *testing.T) {
	notes := gitlabNotesJSON(t,
		gitlabBotNote(20, gitlabMarkedReviewBody("second review - must win")),
		gitlabBotNote(10, gitlabMarkedReviewBody("first review - must not win")),
	)

	body, _ := runFixReviewBodyFetch(t, notes)

	assert.Equal(t, gitlabMarkedReviewBody("second review - must win")+"\n", body)
}

// Only review-bot notes carrying the marker are eligible, and the newest
// eligible one wins: the newest-wins choice never relaxes the author/marker
// filter (#9478 ac_3), for either fetcher.
func TestRunAgentJobScript_ReviewNoteFetchAuthorAndMarkerFilter(t *testing.T) {
	tests := []struct {
		name     string
		notes    []gitlabMRNote
		wantBody string // the note body the fetcher must extract ("" = none)
	}{
		{
			name: "marked note from another author is not eligible",
			notes: []gitlabMRNote{
				gitlabOtherUserNote(20, gitlabMarkedReviewBody("marked note from another author - must not win")),
				gitlabBotNote(10, gitlabMarkedReviewBody("older bot note - must win")),
			},
			wantBody: gitlabMarkedReviewBody("older bot note - must win"),
		},
		{
			name: "bot note without the marker is not eligible",
			notes: []gitlabMRNote{
				gitlabBotNote(20, "bot comment without the review marker"),
				gitlabBotNote(10, gitlabMarkedReviewBody("older marked bot note - must win")),
			},
			wantBody: gitlabMarkedReviewBody("older marked bot note - must win"),
		},
		{
			name: "mixed noise - newest eligible marked bot note wins",
			notes: []gitlabMRNote{
				gitlabBotNote(20, gitlabMarkedReviewBody("newest marked bot note - must win")),
				gitlabOtherUserNote(15, gitlabMarkedReviewBody("marked note from another author")),
				gitlabBotNote(12, "bot comment without the review marker"),
				gitlabBotNote(10, gitlabMarkedReviewBody("older marked bot note")),
			},
			wantBody: gitlabMarkedReviewBody("newest marked bot note - must win"),
		},
		{
			name: "no eligible note at all",
			notes: []gitlabMRNote{
				gitlabOtherUserNote(20, gitlabMarkedReviewBody("marked note from another author")),
				gitlabBotNote(10, "bot comment without the review marker"),
			},
			wantBody: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			notes := gitlabNotesJSON(t, tt.notes...)

			// The body file carries jq's trailing newline when a note was
			// extracted; with no eligible note it stays empty.
			wantFile := ""
			if tt.wantBody != "" {
				wantFile = tt.wantBody + "\n"
			}

			priorBody, provenance, priorOut := runPriorReviewFetch(t, notes)
			assert.Equal(t, wantFile, priorBody, "prior-review fetcher")
			if tt.wantBody == "" {
				assert.Equal(t, "none", provenance)
				assert.Contains(t, priorOut, "No prior review found")
			} else {
				assert.Equal(t, "bot-verified", provenance)
			}

			fixBody, fixOut := runFixReviewBodyFetch(t, notes)
			assert.Equal(t, wantFile, fixBody, "/fs-fix review-body fetcher")
			if tt.wantBody == "" {
				assert.Contains(t, fixOut, "No review note found on MR !3")
			}
		})
	}
}
