package scaffold

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// PR #9490 review (thread 4575375937): gitlabNotesFetcherPreamble points
// REVIEW_BODY_FILE and PRIOR_REVIEW_FILE at the same file, so the fetcher
// tests there cannot detect a change to which output variable a shipped
// section writes — the prior-review branch writes ${PRIOR_REVIEW_FILE} and
// the /fs-fix branch writes ${REVIEW_BODY_FILE}, and a swap in either would
// still land in the one shared file the runners read. These tests give each
// variable its own pre-created empty file and read only the fetcher's own
// output, so a shipped section writing the wrong variable leaves that file
// empty and fails them.

// gitlabNotesFetcherDistinctPreamble is gitlabNotesFetcherPreamble with the
// two output variables pointing at distinct pre-created empty files: an
// unwritten variable reads as an empty body, not a missing-file error, and a
// section writing the wrong variable leaves its fetcher's file empty.
func gitlabNotesFetcherDistinctPreamble(bodyFile, priorFile string) []string {
	return []string{
		"set -euo pipefail",
		`fullsend_gate_curl() { case "$1" in *"sort=desc"*) cat "${NOTES_JSON_FILE}" ;; *) echo "unexpected notes URL: $1" >&2 ; return 1 ;; esac; }`,
		fmt.Sprintf(`BOT_ID="%d"`, gitlabReviewBotID),
		`MR_IID="3"`,
		`FULLSEND_PINNED_API_V4_URL="https://gitlab.example/api/v4"`,
		`FULLSEND_PINNED_PROJECT_ID="42"`,
		`FULLSEND_JOB_TOKEN="job-token"`,
		`REVIEW_BODY_FILE="${OUT_DIR}/` + bodyFile + `"`,
		`PRIOR_REVIEW_FILE="${OUT_DIR}/` + priorFile + `"`,
		`: > "${OUT_DIR}/` + bodyFile + `"`,
		`: > "${OUT_DIR}/` + priorFile + `"`,
		`PRIOR_REVIEW_SHA=""`,
		`PRIOR_REVIEW_PROVENANCE="none"`,
	}
}

// runDistinctPriorReviewFetch runs the shipped prior-review fetcher
// (occurrence 0) with the two output variables pointing at distinct files and
// returns both files' contents, the provenance and the snippet's output.
func runDistinctPriorReviewFetch(t *testing.T, notesJSON string) (priorBody, reviewBody, provenance, output string) {
	t.Helper()
	sections := gitlabNotesFetcherSectionsFor(t)[0]
	lines := append(gitlabNotesFetcherDistinctPreamble("review-body", "prior-review-body"),
		sections.loop, sections.branch,
		`printf '%s' "${PRIOR_REVIEW_PROVENANCE-UNSET}" > "${OUT_DIR}/provenance"`)
	outDir, out := runGitlabNotesFetcherBash(t, strings.Join(lines, "\n"), notesJSON)
	return readOutFile(t, outDir, "prior-review-body"), readOutFile(t, outDir, "review-body"), readOutFile(t, outDir, "provenance"), out
}

// runDistinctFixReviewBodyFetch runs the shipped /fs-fix review-body fetcher
// (occurrence 1) with the two output variables pointing at distinct files and
// returns both files' contents and the snippet's output.
func runDistinctFixReviewBodyFetch(t *testing.T, notesJSON string) (priorBody, reviewBody, output string) {
	t.Helper()
	sections := gitlabNotesFetcherSectionsFor(t)[1]
	lines := append(gitlabNotesFetcherDistinctPreamble("review-body", "prior-review-body"), sections.loop, sections.branch)
	outDir, out := runGitlabNotesFetcherBash(t, strings.Join(lines, "\n"), notesJSON)
	return readOutFile(t, outDir, "prior-review-body"), readOutFile(t, outDir, "review-body"), out
}

// The prior-review fetcher writes its output through PRIOR_REVIEW_FILE only:
// with the two output variables pointing at distinct files, the extracted
// body must land in the prior-review file and leave the review-body file
// empty. A shipped section writing REVIEW_BODY_FILE instead (the wrong
// variable) leaves the prior-review file empty and fails this.
func TestRunAgentJobScript_PriorReviewFetchWritesOnlyItsOutputFile(t *testing.T) {
	t.Run("newest marked bot note lands in the prior-review file only", func(t *testing.T) {
		notes := gitlabNotesJSON(t,
			gitlabBotNote(20, gitlabMarkedReviewBody("second review - must win")),
			gitlabBotNote(10, gitlabMarkedReviewBody("first review - must not win")),
		)

		priorBody, reviewBody, provenance, _ := runDistinctPriorReviewFetch(t, notes)

		assert.Equal(t, gitlabMarkedReviewBody("second review - must win")+"\n", priorBody)
		assert.Equal(t, "", reviewBody, "the prior-review fetcher must not write the /fs-fix review-body output")
		assert.Equal(t, "bot-verified", provenance)
	})

	t.Run("no eligible note leaves both files empty, not missing", func(t *testing.T) {
		notes := gitlabNotesJSON(t,
			gitlabOtherUserNote(20, gitlabMarkedReviewBody("marked note from another author")),
		)

		priorBody, reviewBody, provenance, out := runDistinctPriorReviewFetch(t, notes)

		assert.Equal(t, "", priorBody)
		assert.Equal(t, "", reviewBody)
		assert.Equal(t, "none", provenance)
		assert.Contains(t, out, "No prior review found")
	})
}

// The /fs-fix review-body fetcher writes its output through REVIEW_BODY_FILE
// only: with the two output variables pointing at distinct files, the
// extracted body must land in the review-body file and leave the
// prior-review file empty. A shipped section writing PRIOR_REVIEW_FILE
// instead (the wrong variable) leaves the review-body file empty and fails
// this.
func TestRunAgentJobScript_FixReviewBodyFetchWritesOnlyItsOutputFile(t *testing.T) {
	t.Run("newest marked bot note lands in the review-body file only", func(t *testing.T) {
		notes := gitlabNotesJSON(t,
			gitlabBotNote(20, gitlabMarkedReviewBody("second review - must win")),
			gitlabBotNote(10, gitlabMarkedReviewBody("first review - must not win")),
		)

		priorBody, reviewBody, _ := runDistinctFixReviewBodyFetch(t, notes)

		assert.Equal(t, gitlabMarkedReviewBody("second review - must win")+"\n", reviewBody)
		assert.Equal(t, "", priorBody, "the /fs-fix review-body fetcher must not write the prior-review output")
	})

	t.Run("no eligible note leaves both files empty, not missing", func(t *testing.T) {
		notes := gitlabNotesJSON(t,
			gitlabOtherUserNote(20, gitlabMarkedReviewBody("marked note from another author")),
		)

		priorBody, reviewBody, out := runDistinctFixReviewBodyFetch(t, notes)

		assert.Equal(t, "", priorBody)
		assert.Equal(t, "", reviewBody)
		assert.Contains(t, out, "No review note found on MR !3")
	})
}
