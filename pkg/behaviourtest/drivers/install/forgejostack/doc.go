// Package forgejostack starts, checks, and tears down the managed
// Forgejo and Forgejo Runner stack of ENVIRONMENT=forgejo-dev behaviour
// tests.
//
// The stack runs on the host's Docker or Podman engine, reached through
// its Unix socket (ResolveSocket). The pinned Forgejo and Runner
// versions live in versions.go. Start returns once Forgejo is healthy
// and an Actions job has run in a job container and reached the stack's
// canonical URL; Stack.Teardown removes every container the stack
// started.
package forgejostack
