package steps

import (
	"context"
	"fmt"
	"sort"

	"github.com/cucumber/godog"

	"github.com/fullsend-ai/fullsend/internal/forge"
	scmgh "github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm/github"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

func registerRepoVariableSteps(sc *godog.ScenarioContext) {
	sc.Step(`^the repository variable "([A-Z][A-Z0-9_]*)" is "([^"]*)"$`, func(ctx context.Context, name, value string) (context.Context, error) {
		return ctx, givenRepoVariable(world.FromContext(ctx), name, value)
	})
}

// repoVariableClient returns the forge client of the GitHub SCM driver.
func repoVariableClient(w *world.World) (forge.Client, error) {
	ghDriver, ok := w.SCM.(*scmgh.Driver)
	if !ok {
		return nil, fmt.Errorf("repository variable steps require the GitHub SCM driver")
	}
	return ghDriver.Client, nil
}

// givenRepoVariable sets a repository variable on the enrolled repo. The
// first time a scenario sets a name, the pre-scenario value (or its absence)
// is recorded so RestoreRepoVariables can put it back during cleanup.
func givenRepoVariable(w *world.World, name, value string) error {
	if w.Org == "" || w.RepoName == "" {
		return fmt.Errorf("no repo configured; call 'Given the enrolled test repository' before setting repository variables")
	}
	client, err := repoVariableClient(w)
	if err != nil {
		return err
	}
	if _, recorded := w.RepoVariablesOriginal[name]; !recorded {
		original, exists, err := client.GetRepoVariable(context.Background(), w.Org, w.RepoName, name)
		if err != nil {
			return fmt.Errorf("reading repository variable %s: %w", name, err)
		}
		if w.RepoVariablesOriginal == nil {
			w.RepoVariablesOriginal = map[string]*string{}
		}
		if exists {
			w.RepoVariablesOriginal[name] = &original
		} else {
			w.RepoVariablesOriginal[name] = nil
		}
	}
	if err := client.CreateOrUpdateRepoVariable(mutationCtx(w), w.Org, w.RepoName, name, value); err != nil {
		return fmt.Errorf("setting repository variable %s: %w", name, err)
	}
	return nil
}

// RestoreRepoVariables puts back every repository variable a scenario set:
// the recorded value, or a delete when the variable did not exist. A name is
// forgotten only once it is restored, so a failed restore is retried.
func RestoreRepoVariables(w *world.World) error {
	if len(w.RepoVariablesOriginal) == 0 {
		return nil
	}
	client, err := repoVariableClient(w)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(w.RepoVariablesOriginal))
	for name := range w.RepoVariablesOriginal {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		original := w.RepoVariablesOriginal[name]
		if original == nil {
			err = client.DeleteRepoVariable(mutationCtx(w), w.Org, w.RepoName, name)
			if err != nil && !forge.IsNotFound(err) {
				return fmt.Errorf("deleting repository variable %s: %w", name, err)
			}
		} else if err := client.CreateOrUpdateRepoVariable(mutationCtx(w), w.Org, w.RepoName, name, *original); err != nil {
			return fmt.Errorf("restoring repository variable %s: %w", name, err)
		}
		delete(w.RepoVariablesOriginal, name)
	}
	return nil
}
