package world

import (
	"context"
	"fmt"
	"sync"
)

// RepoPool is an in-process lease pool for logical test-repo names.
// Scenarios acquire a unique name for their duration and release it
// afterward, ensuring no two concurrent scenarios use the same repo.
type RepoPool struct {
	names chan string
	size  int

	mu          sync.Mutex
	outstanding map[string]struct{} // names currently leased
}

// NewRepoPool creates a pool pre-filled with size names in the form
// "test-repo-01" … "test-repo-NN". Returns an error if size is not positive.
func NewRepoPool(size int) (*RepoPool, error) {
	if size <= 0 {
		return nil, fmt.Errorf("pool size must be positive, got %d", size)
	}
	ch := make(chan string, size)
	for i := 1; i <= size; i++ {
		ch <- fmt.Sprintf("test-repo-%02d", i)
	}
	return &RepoPool{
		names:       ch,
		size:        size,
		outstanding: make(map[string]struct{}),
	}, nil
}

// Acquire blocks until a repo name is available or ctx is cancelled.
func (p *RepoPool) Acquire(ctx context.Context) (string, error) {
	select {
	case name := <-p.names:
		p.mu.Lock()
		p.outstanding[name] = struct{}{}
		p.mu.Unlock()
		return name, nil
	case <-ctx.Done():
		return "", fmt.Errorf("acquiring repo name: %w", ctx.Err())
	}
}

// Release returns a previously acquired name to the pool.
// It returns an error if the name is not an outstanding lease (e.g.
// double-release). Callers in godog After hooks should surface the
// error rather than allowing a panic to crash the test runner.
func (p *RepoPool) Release(name string) error {
	p.mu.Lock()
	if _, ok := p.outstanding[name]; !ok {
		p.mu.Unlock()
		return fmt.Errorf("RepoPool: releasing %q which is not an outstanding lease (possible double-release)", name)
	}
	delete(p.outstanding, name)
	p.mu.Unlock()
	p.names <- name
	return nil
}

// Size returns the total capacity of the pool.
func (p *RepoPool) Size() int {
	return p.size
}
