package jirapoll

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"
	"unicode/utf16"
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

// recordOutputSize returns the size counted for one record inside the poll
// job's matrix output. The managed workflow builds that output with
// jq -c '{include: .}', so the record is measured as compact JSON. GitHub
// approximates output size from the UTF-16 encoding, so each UTF-16 code
// unit counts as two bytes. Go writes <, > and & (and U+2028, U+2029) as
// six-character escapes where jq writes the character itself, so for text
// that holds them this count is above what GitHub sees, never below. A
// record that cannot be encoded counts as empty here; writing the dispatch
// file reports it.
func recordOutputSize(record DispatchRecord) int {
	data, err := json.Marshal(record)
	if err != nil {
		return 0
	}
	units := 0
	for _, r := range string(data) {
		units += utf16.RuneLen(r)
	}
	return 2 * units
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
// GitHub target, maxMatrixOutputBytes of matrix output. Within the limits it
// returns the records in the order given. Over a limit it keeps the oldest
// records that fit, oldest first, and drops the rest with a WARNING that
// names the issue. The drop is deliberate, like the event cap in
// processIssue: one issue does not honestly produce that many records in a
// cycle, so the limits bound the runs that a rewound lastCheck or a bulk
// change can start. The caller still advances the checkpoint.
func (p *Poller) capIssueRecords(issueKey string, records []issueRecord) []DispatchRecord {
	if len(records) == 0 {
		return nil
	}
	sized := p.opts.TargetForge != TargetForgeGitLab
	// fit returns how many of the leading records are within the limits.
	fit := func() int {
		size := matrixEnvelopeSize
		for i, r := range records {
			if i == maxRecordsPerIssue {
				return i
			}
			if !sized {
				continue
			}
			if i > 0 {
				size += 2
			}
			if size += recordOutputSize(r.record); size > maxMatrixOutputBytes {
				return i
			}
		}
		return len(records)
	}

	kept := fit()
	if kept < len(records) {
		slices.SortStableFunc(records, func(a, b issueRecord) int { return a.at.Compare(b.at) })
		kept = fit()
		limits := fmt.Sprintf("%d records", maxRecordsPerIssue)
		if sized {
			limits += fmt.Sprintf(" and %d KB of GitHub job output", maxMatrixOutputBytes/1024)
		}
		log.Printf("WARNING: %s produced %d dispatch records this cycle; one issue contributes at most %s, so the oldest %d are kept and %d dropped (possible lastCheck rewind or bulk change)",
			issueKey, len(records), limits, kept, len(records)-kept)
	}

	out := make([]DispatchRecord, kept)
	for i := range out {
		out[i] = records[i].record
	}
	return out
}
