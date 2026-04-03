//go:build e2e

package admin

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// acquireLock attempts to acquire the distributed e2e lock by creating an
// e2e-lock repo in the test org. If the lock is already held, it polls
// until the lock is released or the timeout expires.
//
// The token parameter is needed for getRepoCreatedAt (direct API call).
// Pass "" if using a fake client (skips age checks).
func acquireLock(ctx context.Context, client forge.Client, token, org, runID string, timeout time.Duration) error {
	// Try to create the lock repo.
	acquired, err := tryCreateLock(ctx, client, org, runID)
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
			acquired, err := tryCreateLock(ctx, client, org, runID)
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

		// Check lock age if we have a token (skip for fake clients).
		if token != "" {
			createdAt, ageErr := getRepoCreatedAt(ctx, token, org, lockRepo)
			if ageErr == nil {
				age := time.Since(createdAt)

				// Stale lock recovery.
				if age > timeout {
					fmt.Printf("[e2e-lock] Lock appears stale (age: %s), force-acquiring\n", age)
					_ = client.DeleteRepo(ctx, org, lockRepo)
					acquired, err := tryCreateLock(ctx, client, org, runID)
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
					fmt.Printf("[e2e-lock] Lock recently acquired by another run (age: %s), resetting timer\n", age)
					deadline = time.Now().Add(timeout)
				}

				fmt.Printf("[e2e-lock] Lock held by %s (age: %s), waiting...\n", truncateUUID(holder), age.Round(time.Second))
			}
		} else {
			fmt.Printf("[e2e-lock] Lock held by %s, waiting...\n", truncateUUID(holder))
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
func tryCreateLock(ctx context.Context, client forge.Client, org, runID string) (bool, error) {
	_, err := client.CreateRepo(ctx, org, lockRepo, "E2E test lock — do not delete manually", false)
	if err != nil {
		// Repo already exists (409 or similar) — someone else got it.
		return false, nil
	}

	err = client.CreateFile(ctx, org, lockRepo, "README.md", "acquire lock", []byte(runID))
	if err != nil {
		// Failed to write our UUID — cleanup and report failure.
		_ = client.DeleteRepo(ctx, org, lockRepo)
		return false, fmt.Errorf("writing lock file: %w", err)
	}

	// Verify we actually got the lock (handle race between two creators).
	content, err := client.GetFileContent(ctx, org, lockRepo, "README.md")
	if err != nil {
		return false, fmt.Errorf("verifying lock: %w", err)
	}
	if strings.TrimSpace(string(content)) == runID {
		fmt.Printf("[e2e-lock] Lock acquired (run: %s)\n", truncateUUID(runID))
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
func truncateUUID(uuid string) string {
	if len(uuid) > 8 {
		return uuid[:8]
	}
	return uuid
}
