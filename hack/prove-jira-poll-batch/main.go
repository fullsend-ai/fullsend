// Command prove-jira-poll-batch writes the dispatch file a jira-poll cycle
// produces when a Jira project holds more work than one cycle may take, so
// the batch is as large as the driver's limits allow (ADR 0085, Decision
// items 3 and 4). The full-size batch check
// (.github/workflows/jira-poll-batch-check.yml) passes that file through the
// same hand-off as the managed poll workflow, on GitHub.
//
// The driver is the real one: a jirapoll.Poller reads a fake Jira over HTTP
// and each record is projected as the CLI projects it. Only the Jira content
// and the single agent every comment matches are made up.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harnessdispatch"
	"github.com/fullsend-ai/fullsend/internal/jirapoll"
	"github.com/fullsend-ai/fullsend/internal/normevent"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/jiramock"
)

const (
	// command starts every comment, so each one is an authorized request.
	command = "/fs-triage "
	// maxBody is the number of code points of a comment body the driver
	// carries on the GitHub target.
	maxBody = 4096
)

// batchCase describes the Jira content of one case. Every issue holds one
// comment of the command followed by filler.
type batchCase struct {
	about  string
	issues int    // more than a cycle can take
	filler string // one code point, repeated after the command
	// fillerLen is the number of filler code points, or -1 for the most that
	// still lets a cycle take the record limit.
	fillerLen int
}

var cases = map[string]batchCase{
	"records": {
		about:     "250 records, the record limit, with comments as long as still fit in the size limit",
		issues:    300,
		filler:    "a",
		fillerLen: -1,
	},
	"size": {
		about:     "the largest plain-text records, up to the size limit",
		issues:    60,
		filler:    "a",
		fillerLen: maxBody - len(command),
	},
	"wide": {
		about:     "the largest records of three-byte characters, up to the size limit",
		issues:    60,
		filler:    "語",
		fillerLen: maxBody - len(command),
	},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run builds the batch the arguments name and returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prove-jira-poll-batch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("case", "", "batch to build: "+strings.Join(caseNames(), ", "))
	out := fs.String("out", "dispatches.json", "path of the dispatch file to write")
	targetRepo := fs.String("target-repo", "fullsend-ai/fullsend", "owner/repo the records name as their target")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// The poller logs every warning of a cycle; only the result matters here.
	log.SetOutput(io.Discard)

	records, err := build(*name, *targetRepo, *out)
	if err != nil {
		fmt.Fprintln(stderr, "prove-jira-poll-batch:", err)
		return 1
	}
	fmt.Fprintf(stdout, "case %s: %d records in %s (%s)\n", *name, records, *out, cases[*name].about)
	return 0
}

func caseNames() []string {
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// build writes the dispatch file of the named case to out and returns the
// number of records in it.
func build(name, targetRepo, out string) (int, error) {
	c, ok := cases[name]
	if !ok {
		return 0, fmt.Errorf("unknown case %q: want one of %s", name, strings.Join(caseNames(), ", "))
	}
	if c.fillerLen >= 0 {
		return runCycle(c, c.fillerLen, targetRepo, out)
	}

	// Find the longest comment with which a cycle still takes the record
	// limit: a longer one makes the size limit stop the cycle first.
	limit := config.MaxJiraPollMaxDispatchesPerCycle
	lo, hi := 0, maxBody-len(command)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		records, err := runCycle(c, mid, targetRepo, out)
		if err != nil {
			return 0, err
		}
		if records >= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return runCycle(c, lo, targetRepo, out)
}

// runCycle fills a fake Jira with the case's issues, runs one poll cycle at
// the highest configurable record limit, and returns the number of records
// the cycle wrote to out.
func runCycle(c batchCase, fillerLen int, targetRepo, out string) (int, error) {
	srv, state := jiramock.NewServer()
	defer srv.Close()

	body := command + strings.Repeat(c.filler, fillerLen)
	for i := 1; i <= c.issues; i++ {
		key := fmt.Sprintf("PROJ-%d", i)
		state.AddIssue(key, nil)
		state.AddComment(key, body)
	}

	client, err := jira.New("test-token", jira.WithBaseURL(srv.URL))
	if err != nil {
		return 0, fmt.Errorf("create Jira client: %w", err)
	}
	poller := jirapoll.New(client, oneAgent{}, jirapoll.Options{
		TargetRepo: targetRepo,
		// A fixed site name, so a record's size does not depend on the
		// fake server's port.
		JiraBaseURL:           "https://example.atlassian.net",
		JiraProject:           "PROJ",
		OutputPath:            out,
		MaxDispatchesPerCycle: config.MaxJiraPollMaxDispatchesPerCycle,
	})
	if err := poller.Run(context.Background()); err != nil {
		return 0, fmt.Errorf("poll cycle: %w", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		return 0, err
	}
	var records []json.RawMessage
	if err := json.Unmarshal(data, &records); err != nil {
		return 0, fmt.Errorf("read %s: %w", out, err)
	}
	return len(records), nil
}

// oneAgent matches every authorized event to one agent and projects the
// record with harnessdispatch.ProjectExecutionRef, as the CLI's matcher does
// for each harness an event triggers.
type oneAgent struct{}

func (oneAgent) Match(_ context.Context, event *normevent.Event) ([]jirapoll.DispatchRecord, error) {
	if !harnessdispatch.IsAuthorized(event) {
		return nil, nil
	}
	ref, err := harnessdispatch.ProjectExecutionRef("triage", "triage", event)
	if err != nil {
		return nil, err
	}
	return []jirapoll.DispatchRecord{{
		Agent:         ref.Agent,
		Role:          ref.Role,
		SourceRepo:    ref.SourceRepo,
		EventType:     ref.EventType,
		EventPayload:  ref.EventPayload,
		TriggerSource: ref.TriggerSource,
		StatusRepo:    ref.StatusRepo,
		StatusNumber:  ref.StatusNumber,
	}}, nil
}
