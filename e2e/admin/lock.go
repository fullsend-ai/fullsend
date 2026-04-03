//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// acquireLock attempts to acquire the distributed e2e lock by creating an
// e2e-lock repo in the test org. If the lock is already held, it polls
// until the lock is released or the timeout expires.
//
// The token parameter is needed for getRepoCreatedAt (direct API call).
// Pass "" if using a fake client (skips age checks).
func acquireLock(ctx context.Context, client forge.Client, token, org, runID string, timeout time.Duration, logf func(string, ...any)) error {
	// Try to create the lock repo.
	acquired, err := tryCreateLock(ctx, client, org, runID, logf)
	if err != nil {
		return fmt.Errorf("trying to create lock: %w", err)
	}
	if acquired {
		return nil
	}

	// Lock exists. Poll until released or timeout.
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// Check if lock was released.
		content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
		if forge.IsNotFound(err) {
			// Lock was released — try to acquire.
			acquired, err := tryCreateLock(ctx, client, org, runID, logf)
			if err != nil {
				return fmt.Errorf("retrying lock creation: %w", err)
			}
			if acquired {
				return nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("reading lock file: %w", err)
		}

		holder := strings.TrimSpace(string(content))
		if holder == runID {
			return nil // We hold it.
		}

		// If the lock content is not a valid UUID (e.g. default README
		// content like "# e2e-lock"), treat the lock as stale.
		if !isValidUUID(holder) {
			logf("[e2e-lock] Lock contains invalid holder %q, force-acquiring", truncateUUID(holder))
			_ = client.DeleteRepo(ctx, org, lockRepo)
			acquired, err := tryCreateLock(ctx, client, org, runID, logf)
			if err != nil {
				return fmt.Errorf("force-acquiring invalid lock: %w", err)
			}
			if acquired {
				return nil
			}
			continue
		}

		// Check lock age if we have a token (skip for fake clients).
		if token != "" {
			createdAt, ageErr := getRepoCreatedAt(ctx, token, org, lockRepo)
			if ageErr == nil {
				age := time.Since(createdAt)

				// Stale lock recovery.
				if age > timeout {
					logf("[e2e-lock] Lock appears stale (age: %s), force-acquiring", age)
					_ = client.DeleteRepo(ctx, org, lockRepo)
					acquired, err := tryCreateLock(ctx, client, org, runID, logf)
					if err != nil {
						return fmt.Errorf("force-acquiring stale lock: %w", err)
					}
					if acquired {
						return nil
					}
					continue
				}

				// Fresh lock — reset deadline.
				if age < freshLockThreshold {
					logf("[e2e-lock] Lock recently acquired by another run (age: %s), resetting timer", age)
					deadline = time.Now().Add(timeout)
				}

				logf("[e2e-lock] Lock held by %s (age: %s), waiting...", truncateUUID(holder), age.Round(time.Second))
			}
		} else {
			logf("[e2e-lock] Lock held by %s, waiting...", truncateUUID(holder))
		}

		select {
		case <-time.After(lockPollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return fmt.Errorf("timed out waiting for e2e lock after %s", timeout)
}

// tryCreateLock attempts to create the lock repo and write our UUID.
// Returns (true, nil) if the lock was successfully acquired.
func tryCreateLock(ctx context.Context, client forge.Client, org, runID string, logf func(string, ...any)) (bool, error) {
	_, err := client.CreateRepo(ctx, org, lockRepo, "E2E test lock — do not delete manually", false)
	if err != nil {
		// Repo already exists (409 or similar) — someone else got it.
		return false, nil
	}

	// Use CreateOrUpdateFile since auto_init creates a default README.md.
	// Retry — newly created repos may not be immediately ready for file
	// operations due to GitHub's eventual consistency.
	createErr := retryOnNotFound(ctx, 5, func() error {
		return client.CreateOrUpdateFile(ctx, org, lockRepo, "README.md", "acquire lock", []byte(runID))
	})
	if createErr != nil {
		_ = client.DeleteRepo(ctx, org, lockRepo)
		return false, fmt.Errorf("writing lock file after retries: %w", createErr)
	}

	// Verify we actually got the lock (handle race between two creators).
	content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
	if err != nil {
		return false, fmt.Errorf("verifying lock: %w", err)
	}
	if strings.TrimSpace(string(content)) == runID {
		logf("[e2e-lock] Lock acquired (run: %s)", truncateUUID(runID))
		return true, nil
	}

	// Lost the race.
	return false, nil
}

// releaseLock deletes the lock repo, but only if we still hold it.
func releaseLock(ctx context.Context, client forge.Client, org, runID string, t *testing.T) {
	content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
	if err != nil {
		t.Logf("[e2e-lock] Could not read lock file during release: %v", err)
		return
	}

	if strings.TrimSpace(string(content)) != runID {
		t.Logf("[e2e-lock] Lock is held by someone else (%s), not releasing", truncateUUID(string(content)))
		return
	}

	if err := client.DeleteRepo(ctx, org, lockRepo); err != nil {
		t.Logf("[e2e-lock] Failed to release lock: %v", err)
		return
	}
	t.Logf("[e2e-lock] Lock released (run: %s)", truncateUUID(runID))
}

// truncateUUID returns the first 8 chars of a UUID for log readability.
func truncateUUID(u string) string {
	if len(u) > 8 {
		return u[:8]
	}
	return u
}

// isValidUUID checks whether the string is a valid UUID.
func isValidUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}
