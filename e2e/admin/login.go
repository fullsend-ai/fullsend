//go:build e2e

package admin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// verifyGitHubSession checks that the browser context has a valid GitHub
// session by navigating to a page that requires authentication. If the
// session is expired or invalid, it returns an error.
func verifyGitHubSession(page playwright.Page, screenshotDir string, logf func(string, ...any)) error {
	if _, err := page.Goto("https://github.com/settings/profile", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		return fmt.Errorf("navigating to settings/profile: %w", err)
	}

	url := page.URL()
	logf("[session] Verification URL: %s", url)

	if strings.Contains(url, "/login") || strings.Contains(url, "/session") {
		saveDebugScreenshot(page, screenshotDir, "session-expired", logf)
		return fmt.Errorf("session is not authenticated: navigating to /settings/profile redirected to %s\n\nThe stored browser session has expired. To fix:\n  1. make e2e-export-session   # re-login and export a fresh session\n  2. make e2e-upload-session   # export + upload to GitHub secret", url)
	}

	logf("[session] Session is valid")
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
