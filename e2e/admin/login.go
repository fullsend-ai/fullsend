//go:build e2e

package admin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// githubLogin logs into GitHub by filling in the login form programmatically.
// This eliminates the need for stored browser sessions and manual refresh.
func githubLogin(page playwright.Page, username, password string, logf func(string, ...any)) error {
	if _, err := page.Goto("https://github.com/login", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("navigating to GitHub login: %w", err)
	}

	// Check if already logged in (redirected away from login page).
	if !strings.Contains(page.URL(), "/login") && !strings.Contains(page.URL(), "/session") {
		return nil
	}

	// Fill in credentials.
	if err := page.Locator("#login_field").Fill(username); err != nil {
		return fmt.Errorf("filling username: %w", err)
	}
	if err := page.Locator("#password").Fill(password); err != nil {
		return fmt.Errorf("filling password: %w", err)
	}

	// Submit the form.
	if err := page.Locator("input[type='submit'], button[type='submit']").First().Click(); err != nil {
		return fmt.Errorf("clicking sign in: %w", err)
	}

	// Wait for navigation away from the login/session page.
	if err := page.WaitForURL("https://github.com/**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(7500),
	}); err != nil {
		currentURL := page.URL()
		// If still on login/session, authentication likely failed.
		if strings.Contains(currentURL, "/login") || strings.Contains(currentURL, "/session") {
			return fmt.Errorf("login appears to have failed, still on %s", currentURL)
		}
		// Navigated somewhere else — might be OK.
	}

	// Final check: make sure we're not still on a login page.
	currentURL := page.URL()
	if strings.Contains(currentURL, "/login") || strings.Contains(currentURL, "/sessions/two-factor") {
		return fmt.Errorf("login incomplete, ended up at %s (2FA may be enabled)", currentURL)
	}

	logf("[login] Successfully logged in, current URL: %s", currentURL)
	return nil
}

// saveDebugScreenshot saves a screenshot to dir for debugging.
func saveDebugScreenshot(page playwright.Page, dir, name string, logf func(string, ...any)) {
	path := filepath.Join(dir, fmt.Sprintf("e2e-debug-%s.png", name))
	if _, err := page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(true),
	}); err != nil {
		logf("[debug] Could not save screenshot %s: %v", path, err)
		return
	}
	logf("[debug] Screenshot saved: %s", path)
}
