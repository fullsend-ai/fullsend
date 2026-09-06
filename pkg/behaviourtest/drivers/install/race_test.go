package install

// TestConcurrentStateAccess was removed — the exported State interface
// and PerRepoState type were dropped per #6170. The composedDriver no
// longer manages a pool, so concurrent allocation tests are not
// applicable. The ensurer is concurrency-safe by construction (no
// shared mutable state between CreateRepo calls).
