package world

import (
	"context"
	"fmt"
)

// RepoPool is an in-process lease pool for logical test-repo names.
// Scenarios acquire a unique name for their duration and release it
// afterward, ensuring no two concurrent scenarios use the same repo.
type RepoPool struct {
	names chan string
	size  int
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
	return &RepoPool{names: ch, size: size}, nil
}

// Acquire blocks until a repo name is available or ctx is cancelled.
func (p *RepoPool) Acquire(ctx context.Context) (string, error) {
	select {
	case name := <-p.names:
		return name, nil
	case <-ctx.Done():
		return "", fmt.Errorf("acquiring repo name: %w", ctx.Err())
	}
}

// Release returns a previously acquired name to the pool.
func (p *RepoPool) Release(name string) {
	p.names <- name
}

// Size returns the total capacity of the pool.
func (p *RepoPool) Size() int {
	return p.size
}
