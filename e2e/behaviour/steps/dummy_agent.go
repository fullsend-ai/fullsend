package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/artifacts"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/world"
	"github.com/fullsend-ai/fullsend/internal/runtime"
)

const behaviourModuleRoot = "e2e/behaviour"

func registerDummyAgentSteps(ctx *godog.ScenarioContext, w *world.World) {
	ctx.Step(`^a dummy agent that would:$`, func(table *godog.Table) error {
		return parseDummyAgentTable(w, table)
	})
	ctx.Step(`^the agent will succeed to (.+)$`, func(description string) error {
		return assertAgentSucceeds(w, description)
	})
	ctx.Step(`^the agent will fail to (.+)$`, func(description string) error {
		return assertAgentFails(w, description)
	})
	ctx.Step(`^the agent will output ([^\s]+) with:$`, func(fileName, doc string) error {
		return assertAgentOutput(w, fileName, doc)
	})
}

func parseDummyAgentTable(w *world.World, table *godog.Table) error {
	if len(table.Rows) < 2 {
		return fmt.Errorf("dummy agent table requires a header and at least one row")
	}
	header := table.Rows[0]
	col := map[string]int{}
	for i, cell := range header.Cells {
		col[strings.TrimSpace(cell.Value)] = i
	}
	for _, required := range []string{"description", "op", "args"} {
		if _, ok := col[required]; !ok {
			return fmt.Errorf("dummy agent table missing %q column", required)
		}
	}

	moduleRoot, err := findModuleSubdir(behaviourModuleRoot)
	if err != nil {
		return err
	}

	var ops []runtime.BehaviourOperation
	for _, row := range table.Rows[1:] {
		op := runtime.BehaviourOperation{
			Description: strings.TrimSpace(row.Cells[col["description"]].Value),
			Op:          strings.TrimSpace(row.Cells[col["op"]].Value),
			Args:        strings.TrimSpace(row.Cells[col["args"]].Value),
		}
		if op.Op == "write_fixture" {
			parts := strings.SplitN(op.Args, ",", 2)
			if len(parts) != 2 {
				return fmt.Errorf("write_fixture args must be dest_path, fixture_path")
			}
			fixtureRel := strings.TrimSpace(parts[1])
			fixturePath := filepath.Join(moduleRoot, fixtureRel)
			content, err := os.ReadFile(fixturePath)
			if err != nil {
				return fmt.Errorf("reading fixture %s: %w", fixturePath, err)
			}
			op.Content = string(content)
		}
		ops = append(ops, op)
	}

	script := runtime.BehaviourScript{Ops: ops}
	data, err := yaml.Marshal(script)
	if err != nil {
		return fmt.Errorf("marshaling behaviour script: %w", err)
	}

	message := fmt.Sprintf("behaviour: set dummy agent script (%s)", time.Now().UTC().Format(time.RFC3339))
	if err := w.SCM.CommitFile(context.Background(), w.Install.ConfigOwner(), w.Install.ConfigRepo(), w.BehaviourScriptPath(), message, data); err != nil {
		return fmt.Errorf("committing behaviour script: %w", err)
	}

	w.DummyOps = ops
	return nil
}

func assertAgentSucceeds(w *world.World, description string) error {
	return assertAgentOutcome(w, description, true)
}

func assertAgentFails(w *world.World, description string) error {
	return assertAgentOutcome(w, description, false)
}

func assertAgentOutcome(w *world.World, description string, expectSuccess bool) error {
	if err := ensureArtifacts(w); err != nil {
		return err
	}
	data, err := artifacts.FindBehaviourResults(w.ArtifactDir)
	if err != nil {
		return err
	}
	var results runtime.BehaviourResults
	if err := json.Unmarshal(data, &results); err != nil {
		return fmt.Errorf("parsing behaviour-results.json: %w", err)
	}
	description = strings.TrimSpace(description)
	for _, res := range results.Operations {
		if res.Description != description {
			continue
		}
		if res.Success != expectSuccess {
			return fmt.Errorf("operation %q: expected success=%v, got success=%v (error: %s)", description, expectSuccess, res.Success, res.Error)
		}
		return nil
	}
	return fmt.Errorf("operation %q not found in behaviour-results.json", description)
}

func assertAgentOutput(w *world.World, fileName, doc string) error {
	if err := ensureArtifacts(w); err != nil {
		return err
	}
	fileName = strings.TrimSpace(fileName)
	data, err := artifacts.FindOutputFile(w.ArtifactDir, fileName)
	if err != nil {
		return err
	}
	actual := strings.TrimSpace(string(data))
	expected := strings.TrimSpace(doc)
	if actual != expected {
		return fmt.Errorf("output file %q: expected %q, got %q", fileName, expected, actual)
	}
	return nil
}

func findModuleSubdir(rel string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, rel)
			if st, err := os.Stat(candidate); err == nil && st.IsDir() {
				return candidate, nil
			}
			return "", fmt.Errorf("could not find %s under module root %s", rel, dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("could not find go.mod while searching for %s", rel)
}
