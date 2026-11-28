package jirapoll

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"
)

// Limits on the dispatch records one issue contributes to a cycle (ADR 0085,
// Decision items 3 and 4).
const (
	// maxRecordsPerIssue is GitHub's 256-job matrix limit less a small
	// margin. It applies on both targets, so that there is one rule.
	maxRecordsPerIssue = 250
	// maxMatrixOutputBytes is three quarters of GitHub's 1 MB limit on a
	// job's outputs, measured as matrixOutputSize measures it. It applies on
	// the GitHub target only: GitLab starts each pipeline separately and
	// has no batch output.
	maxMatrixOutputBytes = 768 * 1024
)

// matrixEnvelopeSize is the size of a matrix output that holds no records.
const matrixEnvelopeSize = 2 * len(`{"include":[]}`)

// recordOutputSize returns the size GitHub counts for one record inside the
// poll job's matrix output. The managed workflow builds that output with
// jq -c '{include: .}', so the record is measured as compact JSON.
//
// GitHub counts two bytes for each byte of an output's UTF-8 text. Its
// documentation says only that the size is "approximated based on UTF-16
// encoding"; the rule here was measured on github.com in November 2026,
// where a job is rejected for outputs over 1,048,576 bytes: outputs of
// 262,000 "é", 174,000 "語" and 131,000 "😀" are accepted and outputs a
// thousand characters longer are rejected, as is one of 524,270 "a".
//
// The count is never below what GitHub sees. Go writes <, > and & (and
// U+2028, U+2029) as six-character escapes where jq writes the character
// itself, so text that holds them is counted high. jq writes U+007F as a
// six-character escape where Go writes the character, so each one counts
// for the five bytes Go did not write. A record that cannot be encoded
// counts as empty here; writing the dispatch file reports it.
func recordOutputSize(record DispatchRecord) int {
	data, err := json.Marshal(record)
	if err != nil {
		return 0
	}
	return 2 * (len(data) + 5*bytes.Count(data, []byte{0x7f}))
}

// matrixOutputSize returns the size GitHub counts for the poll job's matrix
// output when it holds records.
func matrixOutputSize(records []DispatchRecord) int {
	size := matrixEnvelopeSize
	for i, record := range records {
		if i > 0 {
			size += 2 // the comma between two records
		}
		size += recordOutputSize(record)
	}
	return size
}

// issueRecord is a dispatch record with the time of the event that produced
// it.
type issueRecord struct {
	record DispatchRecord
	at     time.Time
}

// capIssueRecords applies the per-issue limits to the records one issue
// produced this cycle: at most maxRecordsPerIssue records and, on the
// GitHub target, maxMatrixOutputBytes of matrix output. It keeps the oldest
// records that fit, by the time of the event that produced them, and drops
// the rest with a WARNING that names the issue. The records it keeps stay in
// the order given. The drop is deliberate, like the event cap in
// processIssue: one issue does not honestly produce that many records in a
// cycle, so the limits bound the runs that a rewound lastCheck or a bulk
// change can start. The caller still advances the checkpoint.
func (p *Poller) capIssueRecords(issueKey string, records []issueRecord) []DispatchRecord {
	if len(records) == 0 {
		return nil
	}
	sized := p.opts.TargetForge != TargetForgeGitLab

	// Rank the records oldest first without moving them. Events are not
	// produced in time order: comments come before changelog entries, and
	// an edited comment carries the time of its edit.
	oldestFirst := make([]int, len(records))
	for i := range oldestFirst {
		oldestFirst[i] = i
	}
	slices.SortStableFunc(oldestFirst, func(a, b int) int { return records[a].at.Compare(records[b].at) })

	keep := make([]bool, len(records))
	kept, size := 0, matrixEnvelopeSize
	for _, i := range oldestFirst {
		if kept == maxRecordsPerIssue {
			break
		}
		if sized {
			if kept > 0 {
				size += 2 // the comma between two records
			}
			if size += recordOutputSize(records[i].record); size > maxMatrixOutputBytes {
				break
			}
		}
		keep[i] = true
		kept++
	}

	if kept < len(records) {
		limits := fmt.Sprintf("%d records", maxRecordsPerIssue)
		if sized {
			limits += fmt.Sprintf(" and %d KB of GitHub job output", maxMatrixOutputBytes/1024)
		}
		log.Printf("WARNING: %s produced %d dispatch records this cycle; one issue contributes at most %s, so the oldest %d are kept and %d dropped (possible lastCheck rewind or bulk change)",
			issueKey, len(records), limits, kept, len(records)-kept)
	}

	out := make([]DispatchRecord, 0, kept)
	for i, r := range records {
		if keep[i] {
			out = append(out, r.record)
		}
	}
	return out
}
