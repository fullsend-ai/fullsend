package behaviourtest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	gherkin "github.com/cucumber/gherkin/go/v42"
	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"

	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/steps"
)

// provisioningStepRe matches the one step that leases a fresh pool
// repository (and so runs a fresh install) for a scenario.
var provisioningStepRe = regexp.MustCompile(steps.EnrolledTestRepositoryStep)

// pickleProvisions reports whether a scenario instance (pickle) leases a
// fresh pool repository: one of its steps, background steps included,
// is the enrolled-test-repository step. The suite uses the same check
// before the run (to size the org pool) and in the Before hook (to route
// the scenario), so the two counts cannot disagree.
func pickleProvisions(pickleSteps []*messages.PickleStep) bool {
	for _, step := range pickleSteps {
		if step != nil && provisioningStepRe.MatchString(step.Text) {
			return true
		}
	}
	return false
}

// scenarioInstance is one runnable scenario: a Scenario, or one Examples
// row of a Scenario Outline.
type scenarioInstance struct {
	URI        string
	Name       string
	Tags       []string
	Provisions bool
	// Key identifies the instance by source and content (see pickleKey), so a
	// shard plan computed before the run can be matched against the
	// scenarios godog starts.
	Key string
}

// suiteDemand counts the scenario instances a run will execute.
type suiteDemand struct {
	// Selected is every instance the tag filter selected.
	Selected int
	// Skipped is the selected instances that compatibility tags skip
	// before any step runs (they need no org).
	Skipped int
	// Eligible is the instances that will run (Selected - Skipped).
	Eligible int
	// Provisioning is the eligible instances that lease a fresh repo.
	Provisioning int
}

// summarizeDemand classifies instances. skip returns godog.ErrSkip for
// an instance that compatibility tags skip; any other error (a malformed
// tag) fails the scenario in its Before hook before it is routed to an
// org, so the instance is counted as eligible but never as provisioning.
func summarizeDemand(instances []scenarioInstance, skip func(tags []string) error) suiteDemand {
	var d suiteDemand
	for _, inst := range instances {
		d.Selected++
		err := skip(inst.Tags)
		if errors.Is(err, godog.ErrSkip) {
			d.Skipped++
			continue
		}
		d.Eligible++
		if err == nil && inst.Provisions {
			d.Provisioning++
		}
	}
	return d
}

// loadScenarioInstances parses the feature files under paths the way
// godog does (directories are walked for *.feature files in lexical
// order, a "file.feature:LINE" path selects the scenario at LINE,
// duplicate URIs are read once) and returns the instances tagFilter
// (GODOG_TAGS syntax) selects.
func loadScenarioInstances(paths []string, tagFilter string) ([]scenarioInstance, error) {
	seen := make(map[string]bool)
	var out []scenarioInstance
	newID := (&messages.Incrementing{}).NewId
	for _, p := range paths {
		path, line := splitFeaturePathLine(p)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("feature path %q: %w", p, err)
		}
		var files []string
		if info.IsDir() {
			walkErr := filepath.WalkDir(path, func(f string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() && strings.HasSuffix(f, ".feature") {
					files = append(files, f)
				}
				return nil
			})
			if walkErr != nil {
				return nil, fmt.Errorf("walking feature path %q: %w", p, walkErr)
			}
			line = -1
		} else {
			files = []string{path}
		}
		for _, f := range files {
			uri := f
			if line != -1 {
				uri += ":" + strconv.Itoa(line)
			}
			insts, err := parseFeatureInstances(f, uri, line, newID)
			if err != nil {
				return nil, err
			}
			// Godog parses a repeated path before discarding its duplicate
			// URI. Consume the same IDs so later files keep matching IDs.
			if seen[uri] {
				continue
			}
			seen[uri] = true
			for _, inst := range insts {
				if matchTagFilter(tagFilter, inst.Tags) {
					out = append(out, inst)
				}
			}
		}
	}
	return out, nil
}

// parseFeatureInstances compiles one feature file into scenario
// instances. line, when not -1, keeps only the scenario declared there.
func parseFeatureInstances(path, uri string, line int, newID func() string) ([]scenarioInstance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening feature %q: %w", path, err)
	}
	defer f.Close()
	doc, err := gherkin.ParseGherkinDocument(f, newID)
	if err != nil {
		return nil, fmt.Errorf("parsing feature %q: %w", path, err)
	}
	scenarioLines := scenarioLinesByID(doc)
	var out []scenarioInstance
	for _, pickle := range gherkin.Pickles(*doc, uri, newID) {
		if line != -1 && (len(pickle.AstNodeIds) == 0 || scenarioLines[pickle.AstNodeIds[0]] != int64(line)) {
			continue
		}
		tags := make([]string, 0, len(pickle.Tags))
		for _, tag := range pickle.Tags {
			tags = append(tags, tag.Name)
		}
		out = append(out, scenarioInstance{
			URI:        uri,
			Name:       pickle.Name,
			Tags:       tags,
			Provisions: pickleProvisions(pickle.Steps),
			Key:        pickleKey(pickle),
		})
	}
	return out, nil
}

// scenarioLinesByID maps each scenario's AST node ID to its line.
func scenarioLinesByID(doc *messages.GherkinDocument) map[string]int64 {
	lines := make(map[string]int64)
	if doc.Feature == nil {
		return lines
	}
	add := func(sc *messages.Scenario) {
		if sc != nil && sc.Location != nil {
			lines[sc.Id] = sc.Location.Line
		}
	}
	for _, child := range doc.Feature.Children {
		add(child.Scenario)
		if child.Rule != nil {
			for _, rc := range child.Rule.Children {
				add(rc.Scenario)
			}
		}
	}
	return lines
}

var featurePathLineRe = regexp.MustCompile(`:(\d+)$`)

// splitFeaturePathLine splits "file.feature:12" into its path and line
// (-1 when the path carries no line), as godog does.
func splitFeaturePathLine(p string) (string, int) {
	m := featurePathLineRe.FindStringSubmatch(p)
	if m == nil {
		return p, -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return p, -1
	}
	return p[:strings.LastIndexByte(p, ':')], n
}

// matchTagFilter applies a godog tag expression (GODOG_TAGS) to tags
// with godog's semantics: "," separates alternatives, "&&" joins
// required terms, "~" negates a term, and "@" is optional. An empty
// filter matches everything; an empty term matches nothing (godog
// rejects it).
func matchTagFilter(filter string, tags []string) bool {
	if filter == "" {
		return true
	}
	has := func(tag string) bool {
		for _, t := range tags {
			if strings.ReplaceAll(t, "@", "") == tag {
				return true
			}
		}
		return false
	}
	for alternative := range strings.SplitSeq(filter, ",") {
		matched := true
		for term := range strings.SplitSeq(alternative, "&&") {
			term = strings.ReplaceAll(strings.TrimSpace(term), "@", "")
			if term == "" {
				matched = false
				break
			}
			if strings.HasPrefix(term, "~") {
				matched = matched && !has(term[1:])
			} else {
				matched = matched && has(term)
			}
		}
		if matched {
			return true
		}
	}
	return false
}
