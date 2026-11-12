// Package retryselect chooses which feature paths a retried behaviour run
// executes. scripts/run-with-infra-retry.sh retries an
// infrastructure-classified run once; rerunning the whole suite takes as
// long as the first attempt and does not fit the merge queue's check
// window, so the retry reruns only the scenarios attempt 1 recorded as
// failed.
//
// The wrapper passes attempt 1's failure-class report through EnvVar. Each
// failed scenario's record names its feature file and scenario; Select
// parses that file and returns a godog "path:line" entry for every
// scenario with that name. A name it cannot find, for example one changed
// by redaction, falls back to the whole feature file. A report with a
// bootstrap failure, or a scenario failure without a feature file, falls
// back to the full suite, since nothing narrower is known to be enough.
package retryselect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/fullsend-ai/fullsend/internal/behaviourfail"
)

// EnvVar names the path of attempt 1's failure-class report. The
// infra-retry wrapper sets it for attempt 2 only.
const EnvVar = "BEHAVIOUR_RETRY_FROM"

// scenarioSource is the FailureRecord source of a failed scenario.
const scenarioSource = "scenario"

// Selection is the set of feature paths a run executes.
type Selection struct {
	// Paths are the godog feature paths to run.
	Paths []string

	// Targeted is true when Paths names only the failed scenarios or
	// their feature files, and false when the run executes the full
	// suite.
	Targeted bool

	// Reason explains a full-suite selection on a retry; it is empty
	// when Targeted is true or when the run is not a retry.
	Reason string
}

// Select returns the feature paths for a run. reportPath is attempt 1's
// failure-class report, or "" when the run is not a retry; then the
// suite's own featurePaths run unchanged. fsys must resolve feature files
// the way godog does, by the same relative paths.
//
// An unreadable or malformed report, a report whose class is not
// infrastructure, or a failed scenario outside featurePaths is an error:
// the wrapper only retries infrastructure-classified runs, so any of
// these means the report is not the one the retry was granted for.
func Select(fsys fs.FS, featurePaths []string, reportPath string) (Selection, error) {
	if reportPath == "" {
		return Selection{Paths: featurePaths}, nil
	}
	report, err := readReport(reportPath)
	if err != nil {
		return Selection{}, err
	}
	if report.Class != behaviourfail.Infrastructure {
		return Selection{}, fmt.Errorf("retry report %s has class %q, want %q", reportPath, report.Class, behaviourfail.Infrastructure)
	}

	full := func(reason string) (Selection, error) {
		return Selection{Paths: featurePaths, Reason: reason}, nil
	}
	if len(report.Failures) == 0 {
		return full("attempt 1 recorded no failures")
	}

	// names maps each failed feature file to the failed scenario names in it.
	names := map[string]map[string]bool{}
	for _, f := range report.Failures {
		if f.Source != scenarioSource {
			return full("attempt 1 failed outside a scenario (" + f.Source + ")")
		}
		if f.Feature == "" {
			return full("a failed scenario in attempt 1 has no feature file")
		}
		if !underAny(f.Feature, featurePaths) {
			return Selection{}, fmt.Errorf("retry report %s names feature %q outside the suite's paths %v", reportPath, f.Feature, featurePaths)
		}
		if names[f.Feature] == nil {
			names[f.Feature] = map[string]bool{}
		}
		names[f.Feature][f.Scenario] = true
	}

	features := make([]string, 0, len(names))
	for feature := range names {
		features = append(features, feature)
	}
	sort.Strings(features)

	var paths []string
	for _, feature := range features {
		lines, err := scenarioLines(fsys, feature, names[feature])
		if err != nil {
			return Selection{}, err
		}
		if lines == nil {
			// A name was not found: rerun the whole file rather than miss
			// a failed scenario.
			paths = append(paths, feature)
			continue
		}
		for _, line := range lines {
			paths = append(paths, feature+":"+strconv.FormatInt(line, 10))
		}
	}
	return Selection{Paths: paths, Targeted: true}, nil
}

// readReport decodes a failure-class report.
func readReport(reportPath string) (behaviourfail.Report, error) {
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return behaviourfail.Report{}, fmt.Errorf("reading retry report: %w", err)
	}
	var report behaviourfail.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return behaviourfail.Report{}, fmt.Errorf("parsing retry report %s: %w", reportPath, err)
	}
	return report, nil
}

// underAny reports whether feature is one of roots or inside one of them.
// Both sides are slash-separated godog paths.
func underAny(feature string, roots []string) bool {
	if !fs.ValidPath(feature) {
		return false
	}
	for _, root := range roots {
		root = path.Clean(root)
		if root == "." || feature == root || strings.HasPrefix(feature, root+"/") {
			return true
		}
	}
	return false
}

// scenarioLines returns the sorted, distinct lines of the scenarios in
// feature whose pickles carry one of names, or nil when any name matches
// no pickle. A Scenario Outline's pickles take their names from the
// example rows, so a failed example row selects its outline's line, as
// godog's own "path:line" filter does.
func scenarioLines(fsys fs.FS, feature string, names map[string]bool) ([]int64, error) {
	data, err := fs.ReadFile(fsys, feature)
	if err != nil {
		return nil, fmt.Errorf("reading feature %s: %w", feature, err)
	}
	newID := (&messages.Incrementing{}).NewId
	doc, err := gherkin.ParseGherkinDocument(bytes.NewReader(data), newID)
	if err != nil {
		return nil, fmt.Errorf("parsing feature %s: %w", feature, err)
	}
	if doc.Feature == nil {
		return nil, errors.New("feature " + feature + " has no Feature")
	}

	scenarioLine := map[string]int64{}
	for _, child := range doc.Feature.Children {
		if child.Scenario != nil {
			scenarioLine[child.Scenario.Id] = child.Scenario.Location.Line
		}
		if child.Rule != nil {
			for _, rc := range child.Rule.Children {
				if rc.Scenario != nil {
					scenarioLine[rc.Scenario.Id] = rc.Scenario.Location.Line
				}
			}
		}
	}

	found := map[string]bool{}
	seen := map[int64]bool{}
	var lines []int64
	for _, pickle := range gherkin.Pickles(*doc, feature, newID) {
		if !names[pickle.Name] || len(pickle.AstNodeIds) == 0 {
			continue
		}
		line, ok := scenarioLine[pickle.AstNodeIds[0]]
		if !ok {
			continue
		}
		found[pickle.Name] = true
		if !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	for name := range names {
		if !found[name] {
			return nil, nil
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i] < lines[j] })
	return lines, nil
}
