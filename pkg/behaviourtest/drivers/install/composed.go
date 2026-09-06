package install

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	d.logRateLimit("after CreateRepo")
	return name, nil
}

func (d *composedDriver) MarkDeleted(repoName string) {
	d.mu.Lock()
	delete(d.created, repoName)
	d.mu.Unlock()
}

const repoKeepThreshold = 200

func (d *composedDriver) Finalize(ctx context.Context) error {
	d.queryRateLimit("Finalize start")

	if !KeepRepos() {
		d.pruneOldRuns(ctx)
	}

	if d.mint == nil {
		return nil
	}
	if err := d.mint.Teardown(ctx); err != nil {
		return fmt.Errorf("Finalize: mint teardown: %w", err)
	}
	return nil
}

func (d *composedDriver) pruneOldRuns(ctx context.Context) {
	lister, ok := d.client.(forge.AllRepoLister)
	if !ok {
		d.logf("[driver] prune: client does not support AllRepoLister, skipping")
		return
	}

	repos, err := lister.ListAllOrgRepos(ctx, d.org)
	if err != nil {
		d.logf("[driver] prune: failed to list repos: %v", err)
		return
	}

	groups := make(map[string][]string)
	for _, r := range repos {
		if !strings.HasPrefix(r.Name, "bt-") {
			continue
		}
		if len(r.Name) < 19 {
			continue
		}
		ts := r.Name[3:18]
		groups[ts] = append(groups[ts], r.Name)
	}

	total := 0
	for _, names := range groups {
		total += len(names)
	}

	if total <= repoKeepThreshold {
		d.logf("[driver] prune: %d bt-* repos, at or under threshold (%d), skipping", total, repoKeepThreshold)
		return
	}

	timestamps := make([]string, 0, len(groups))
	for ts := range groups {
		timestamps = append(timestamps, ts)
	}
	sort.Strings(timestamps)

	for _, ts := range timestamps {
		names := groups[ts]
		if total-len(names) < repoKeepThreshold {
			break
		}
		d.logf("[driver] prune: deleting run %s (%d repos)", ts, len(names))
		for _, name := range names {
			if err := d.client.DeleteRepo(ctx, d.org, name); err != nil {
				if !forge.IsNotFound(err) {
					d.logf("[driver] prune: failed to delete %s/%s: %v", d.org, name, err)
				}
			}
		}
		total -= len(names)
	}

	d.logf("[driver] prune: %d bt-* repos remaining", total)
	d.queryRateLimit("Finalize after cleanup")
}

func (d *composedDriver) DefaultConcurrency() int {
	return DefaultConcurrencyValue
}

func (d *composedDriver) logRateLimit(label string) {
	r, ok := d.client.(forge.RateLimitReporter)
	if !ok {
		return
	}
	rl, seen := r.RateLimit()
	if !seen {
		return
	}
	d.logf("[rate-limit] %s: %s", label, rl.String())
}

func (d *composedDriver) queryRateLimit(label string) {
	q, ok := d.client.(forge.RateLimitQuerier)
	if !ok {
		return
	}
	rl, err := q.GetRateLimit(context.Background())
	if err != nil {
		d.logf("[rate-limit] %s: query failed: %v", label, err)
		return
	}
	d.logf("[rate-limit] %s: %s", label, rl.String())
}

var _ Driver = (*composedDriver)(nil)
