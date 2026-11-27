package behaviour_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	gherkin "github.com/cucumber/gherkin/go/v42"
	messages "github.com/cucumber/messages/go/v34"
)

// The shard files, relative to this directory. Their entries are relative to
// this directory too, e.g. "features/triage/triage.feature:6".
const (
	shardFeaturesDir   = "features"
	shardsListFile     = "github-emulator/shards.txt"
	notShardedListFile = "github-emulator/not-sharded.txt"
)

// TestShardsCoverEveryScenario checks shards.txt, which lists the scenarios
// that run on pull requests, against the feature files: every scenario must
// be in exactly one shard or in not-sharded.txt, and every entry must point
// at a scenario header.
func TestShardsCoverEveryScenario(t *testing.T) {
	problems, err := shardListProblems(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestShardListProblems(t *testing.T) {
	base := map[string]string{
		"features/a.feature": "Feature: a\n" +
			"\n" +
			"  Scenario: one\n" +
			"    Given x\n" +
			"\n" +
			"  Scenario: two\n" +
			"    Given y\n",
		"features/outline.feature": "Feature: outline\n" +
			"\n" +
			"  Scenario Outline: row <n>\n" +
			"    Given <n>\n" +
			"\n" +
			"    Examples:\n" +
			"      | n |\n" +
			"      | 1 |\n" +
			"      | 2 |\n",
		"features/sub/c.feature": "Feature: c\n" +
			"\n" +
			"  Rule: r\n" +
			"    Scenario: three\n" +
			"      Given z\n" +
			"\n" +
			"  Scenario: four\n" +
			"    Given w\n",
		shardsListFile: "# <shard> <workers> <runners> <feature[:line]>...\n" +
			"x 2 4 features/a.feature:3 features/outline.feature:3\n" +
			"\n" +
			"y 1 1 features/a.feature:6\n",
		notShardedListFile: "# not sharded\n" +
			"features/sub/c.feature # whole feature, Rule included\n",
	}

	tests := []struct {
		name  string
		files map[string]string
		want  []string // substrings, one per problem, in order
	}{
		{
			name: "valid layout",
		},
		{
			name: "unlisted scenario",
			files: map[string]string{
				"features/a.feature": base["features/a.feature"] +
					"\n" +
					"  Scenario: new\n" +
					"    Given n\n",
			},
			want: []string{"features/a.feature:9 is in neither shards.txt nor not-sharded.txt"},
		},
		{
			name: "unlisted feature file",
			files: map[string]string{
				"features/new.feature": "Feature: new\n\n  Scenario: new\n    Given n\n",
			},
			want: []string{"features/new.feature:3 is in neither"},
		},
		{
			name: "blank line above sharded scenarios makes entries stale",
			files: map[string]string{
				"features/a.feature": strings.Replace(base["features/a.feature"], "Feature: a\n", "Feature: a\n\n", 1),
			},
			want: []string{
				"shards.txt:2 (shard x): features/a.feature:3 matches no Scenario or Scenario Outline header (next header: line 4)",
				"shards.txt:4 (shard y): features/a.feature:6 matches no Scenario or Scenario Outline header (next header: line 7)",
				"features/a.feature:4 is in neither",
				"features/a.feature:7 is in neither",
			},
		},
		{
			name: "scenario in two shards",
			files: map[string]string{
				shardsListFile: "x 2 4 features/a.feature:3 features/outline.feature:3\n" +
					"y 1 1 features/a.feature:6 features/a.feature:3\n",
			},
			want: []string{"features/a.feature:3 is listed more than once: shards.txt:1 (shard x), shards.txt:2 (shard y)"},
		},
		{
			name: "scenario in a shard and in not-sharded.txt",
			files: map[string]string{
				notShardedListFile: base[notShardedListFile] + "features/a.feature:6 # also sharded\n",
			},
			want: []string{"features/a.feature:6 is listed more than once: shards.txt:4 (shard y), not-sharded.txt:3"},
		},
		{
			name: "bare feature entry overlaps a line entry",
			files: map[string]string{
				notShardedListFile: base[notShardedListFile] + "features/outline.feature\n",
			},
			want: []string{"features/outline.feature:3 is listed more than once"},
		},
		{
			name: "missing feature file",
			files: map[string]string{
				shardsListFile: base[shardsListFile] + "z 1 1 features/gone.feature:1\n",
			},
			want: []string{"shards.txt:5 (shard z): features/gone.feature:1 names a missing feature file"},
		},
		{
			name: "missing feature file in not-sharded.txt",
			files: map[string]string{
				notShardedListFile: base[notShardedListFile] + "features/gone.feature\n",
			},
			want: []string{"not-sharded.txt:3: features/gone.feature names a missing feature file"},
		},
		{
			name: "Scenario Outline counts once, at its own line",
			files: map[string]string{
				shardsListFile: "x 2 4 features/a.feature:3 features/outline.feature:8\n" +
					"y 1 1 features/a.feature:6\n",
			},
			want: []string{
				"shards.txt:1 (shard x): features/outline.feature:8 matches no Scenario or Scenario Outline header",
				"features/outline.feature:3 is in neither",
			},
		},
		{
			name: "malformed shard line",
			files: map[string]string{
				shardsListFile: base[shardsListFile] + "z 1 features/a.feature:3\n",
			},
			want: []string{"shards.txt:5: want <shard> <workers> <runners> <feature[:line]>..."},
		},
		{
			name: "bad line number",
			files: map[string]string{
				notShardedListFile: base[notShardedListFile] + "features/a.feature:x\n",
			},
			want: []string{"not-sharded.txt:3: features/a.feature:x has a bad line number"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, content := range base {
				if override, ok := tt.files[name]; ok {
					content = override
				}
				writeShardFixture(t, root, name, content)
			}
			for name, content := range tt.files {
				if _, ok := base[name]; !ok {
					writeShardFixture(t, root, name, content)
				}
			}

			got, err := shardListProblems(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d problems, want %d:\n%s", len(got), len(tt.want), strings.Join(got, "\n"))
			}
			for i, want := range tt.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

func TestShardListProblemsMissingListFile(t *testing.T) {
	root := t.TempDir()
	writeShardFixture(t, root, "features/a.feature", "Feature: a\n")
	writeShardFixture(t, root, shardsListFile, "")
	if _, err := shardListProblems(root); err == nil {
		t.Fatal("want an error when not-sharded.txt is missing")
	}
}

func writeShardFixture(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// shardScenario is one scenario header: a feature path relative to the
// root and the line of its Scenario or Scenario Outline keyword.
type shardScenario struct {
	feature string
	line    int
}

func (s shardScenario) String() string { return fmt.Sprintf("%s:%d", s.feature, s.line) }

// shardListProblems checks shards.txt and not-sharded.txt under root against
// the feature files under root/features and returns one message per
// offending entry or scenario. It returns an error only when a file cannot
// be read or parsed.
func shardListProblems(root string) ([]string, error) {
	fsys := os.DirFS(root)
	headers, err := shardScenarioHeaders(fsys)
	if err != nil {
		return nil, err
	}

	var problems []string
	listed := map[shardScenario][]string{}
	for _, list := range []struct {
		name    string
		sharded bool
	}{{shardsListFile, true}, {notShardedListFile, false}} {
		data, err := fs.ReadFile(fsys, list.name)
		if err != nil {
			return nil, err
		}
		problems = append(problems, checkShardList(path.Base(list.name), data, list.sharded, headers, listed)...)
	}

	var scenarios []shardScenario
	for feature, lines := range headers {
		for _, line := range lines {
			scenarios = append(scenarios, shardScenario{feature, line})
		}
	}
	sort.Slice(scenarios, func(i, j int) bool {
		if scenarios[i].feature != scenarios[j].feature {
			return scenarios[i].feature < scenarios[j].feature
		}
		return scenarios[i].line < scenarios[j].line
	})
	for _, s := range scenarios {
		switch where := listed[s]; {
		case len(where) == 0:
			problems = append(problems, fmt.Sprintf("%s is in neither %s nor %s: add it to a shard, or to %s with the reason it cannot run on the emulator",
				s, path.Base(shardsListFile), path.Base(notShardedListFile), path.Base(notShardedListFile)))
		case len(where) > 1:
			problems = append(problems, fmt.Sprintf("%s is listed more than once: %s", s, strings.Join(where, ", ")))
		}
	}
	return problems, nil
}

// checkShardList records in listed where each scenario named by a shard list
// is listed, and returns the problems with its entries. A sharded list's
// lines are "<shard> <workers> <runners> <feature[:line]>..."; the other
// list's lines are "<feature[:line]>...". "#" starts a comment. An entry
// without ":line" covers every scenario in its feature.
func checkShardList(name string, data []byte, sharded bool, headers map[string][]int, listed map[shardScenario][]string) []string {
	var problems []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; scanner.Scan(); n++ {
		text := scanner.Text()
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = text[:i]
		}
		fields := strings.Fields(text)
		if len(fields) == 0 {
			continue
		}
		where := fmt.Sprintf("%s:%d", name, n)
		entries := fields
		if sharded {
			if len(fields) < 4 {
				problems = append(problems, fmt.Sprintf("%s: want <shard> <workers> <runners> <feature[:line]>..., got %q", where, strings.TrimSpace(text)))
				continue
			}
			where = fmt.Sprintf("%s (shard %s)", where, fields[0])
			entries = fields[3:]
		}
		for _, entry := range entries {
			feature, line := entry, 0
			if i := strings.LastIndexByte(entry, ':'); i >= 0 {
				l, err := strconv.Atoi(entry[i+1:])
				if err != nil || l <= 0 {
					problems = append(problems, fmt.Sprintf("%s: %s has a bad line number", where, entry))
					continue
				}
				feature, line = entry[:i], l
			}
			lines, ok := headers[feature]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: %s names a missing feature file", where, entry))
				continue
			}
			if line == 0 {
				for _, l := range lines {
					s := shardScenario{feature, l}
					listed[s] = append(listed[s], where)
				}
				continue
			}
			i := sort.SearchInts(lines, line)
			if i == len(lines) || lines[i] != line {
				msg := fmt.Sprintf("%s: %s matches no Scenario or Scenario Outline header", where, entry)
				if i < len(lines) {
					msg += fmt.Sprintf(" (next header: line %d)", lines[i])
				}
				problems = append(problems, msg)
				continue
			}
			s := shardScenario{feature, line}
			listed[s] = append(listed[s], where)
		}
	}
	return problems
}

// shardScenarioHeaders returns the sorted scenario header lines of every
// feature file under features, keyed by its slash path. A Scenario Outline
// is one header, at its own line, whatever its example rows.
func shardScenarioHeaders(fsys fs.FS) (map[string][]int, error) {
	headers := map[string][]int{}
	err := fs.WalkDir(fsys, shardFeaturesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".feature" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		doc, err := gherkin.ParseGherkinDocument(bytes.NewReader(data), (&messages.Incrementing{}).NewId)
		if err != nil {
			return fmt.Errorf("parsing feature %s: %w", p, err)
		}
		if doc.Feature == nil {
			return errors.New("feature " + p + " has no Feature")
		}
		lines := []int{}
		for _, child := range doc.Feature.Children {
			if child.Scenario != nil {
				lines = append(lines, int(child.Scenario.Location.Line))
			}
			if child.Rule != nil {
				for _, rc := range child.Rule.Children {
					if rc.Scenario != nil {
						lines = append(lines, int(rc.Scenario.Location.Line))
					}
				}
			}
		}
		sort.Ints(lines)
		headers[p] = lines
		return nil
	})
	return headers, err
}
