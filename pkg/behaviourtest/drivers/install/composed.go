package install

import (
	"context"
	"fmt"
	"sync"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// composedDriver wraps a mintDriver, ensurer, and forge client into a
// unified Driver. Scenarios call CreateRepo to provision an ephemeral
// repo; deletion is handled by CleanupScenario via the SCM driver.
// The composedDriver tracks every repo it creates so Finalize can
// sweep any that were not cleaned up (e.g. cancelled CI jobs).
type composedDriver struct {
	org     string
	mint    mintDriver
	ensurer ensurer
	client  forge.Client
	logf    func(string, ...any)

	mu      sync.Mutex
	created map[string]bool
}

func newComposedDriver(
	org string,
	mint mintDriver,
	ens ensurer,
	client forge.Client,
	logf func(string, ...any),
) Driver {
	return &composedDriver{
		org:     org,
		mint:    mint,
		ensurer: ens,
		client:  client,
		logf:    logf,
		created: make(map[string]bool),
	}
}

func (d *composedDriver) CreateRepo(ctx context.Context, hint string) (string, error) {
	name, err := d.ensurer.CreateRepo(ctx, d.org, hint)
	if err != nil {
		return "", fmt.Errorf("creating repo in %s: %w", d.org, err)
	}
	d.mu.Lock()
	d.created[name] = true
	d.mu.Unlock()
	d.logf("[driver] created %s/%s", d.org, name)
	return name, nil
}

func (d *composedDriver) MarkDeleted(repoName string) {
	d.mu.Lock()
	delete(d.created, repoName)
	d.mu.Unlock()
}

func (d *composedDriver) Finalize(ctx context.Context) error {
	if !KeepRepos() {
		d.mu.Lock()
		remaining := make([]string, 0, len(d.created))
		for name := range d.created {
			remaining = append(remaining, name)
		}
		d.mu.Unlock()

		for _, name := range remaining {
			d.logf("[driver] finalize: deleting orphaned %s/%s", d.org, name)
			if err := d.client.DeleteRepo(ctx, d.org, name); err != nil {
				if !forge.IsNotFound(err) {
					d.logf("[driver] finalize: failed to delete %s/%s: %v", d.org, name, err)
				}
			}
		}
	}

	if d.mint == nil {
		return nil
	}
	if err := d.mint.Teardown(ctx); err != nil {
		return fmt.Errorf("Finalize: mint teardown: %w", err)
	}
	return nil
}

func (d *composedDriver) DefaultConcurrency() int {
	return DefaultConcurrencyValue
}

var _ Driver = (*composedDriver)(nil)
