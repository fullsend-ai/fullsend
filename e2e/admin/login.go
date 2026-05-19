//go:build e2e

package admin

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/fullsend-ai/fullsend/e2e/internal/otp"
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

// handleSudoIfPresent detects GitHub's "Confirm access" sudo page and
// enters the password (or TOTP code if 2FA is enabled) to proceed.
// GitHub requires sudo confirmation when accessing sensitive settings pages
// (token management, app settings) even with a valid session.
// Returns true if sudo was handled.
func handleSudoIfPresent(page playwright.Page, password, totpSecret, screenshotDir string, logf func(string, ...any)) (bool, error) {
	pageTitle, _ := page.Title()
	if !strings.Contains(pageTitle, "Confirm access") && !strings.Contains(pageTitle, "Sudo") {
		return false, nil
	}

	logf("[sudo] Detected sudo confirmation page (title: %s)", pageTitle)

	// GitHub may show a password field or a TOTP field (or both with a toggle).
	// Try password first, fall back to TOTP.
	passwordInput := page.Locator("#sudo_password")
	passwordVisible := passwordInput.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(2000),
	}) == nil

	if passwordVisible && password != "" {
		if err := passwordInput.Fill(password); err != nil {
			return false, fmt.Errorf("filling sudo password: %w", err)
		}
	} else if totpSecret != "" {
		if handled, err := handleTOTPIfPresent(page, totpSecret, screenshotDir, logf); err != nil {
			return false, fmt.Errorf("TOTP on sudo page: %w", err)
		} else if !handled {
			saveDebugScreenshot(page, screenshotDir, "sudo-no-auth-method", logf)
			return false, fmt.Errorf("sudo page has no visible password or TOTP field")
		}
		return true, nil
	} else {
		saveDebugScreenshot(page, screenshotDir, "sudo-no-credentials", logf)
		return false, fmt.Errorf("sudo confirmation required but no password or TOTP secret available — set E2E_GITHUB_PASSWORD or E2E_GITHUB_TOTP_SECRET")
	}

	// Click the confirm button (password path).
	confirmBtn := page.Locator("button[type='submit']:has-text('Confirm'), button[type='submit']:has-text('Confirm password'), button[type='submit']")
	if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "sudo-confirm-click-failed", logf)
		return false, fmt.Errorf("clicking sudo confirm button: %w", err)
	}

	if err := waitForPageToLeave(page, "Confirm access", "Sudo"); err != nil {
		saveDebugScreenshot(page, screenshotDir, "sudo-still-on-page", logf)
		return false, err
	}

	logf("[sudo] Sudo confirmation succeeded")
	return true, nil
}

// handleTOTPIfPresent detects a GitHub 2FA/TOTP input on the current page
// and fills in a generated code. Works on both the post-login 2FA page
// (/sessions/two-factor) and the sudo TOTP prompt. Returns true if a TOTP
// form was found and submitted.
func handleTOTPIfPresent(page playwright.Page, totpSecret, screenshotDir string, logf func(string, ...any)) (bool, error) {
	// GitHub uses #app_totp for the TOTP input on both 2FA and sudo pages.
	totpInput := page.Locator("#app_totp")
	if err := totpInput.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(3000),
	}); err != nil {
		return false, nil
	}

	logf("[totp] Detected TOTP input on page (URL: %s)", page.URL())

	code, err := otp.GenerateCode(totpSecret)
	if err != nil {
		return false, fmt.Errorf("generating TOTP code: %w", err)
	}

	// Use PressSequentially to simulate keystroke entry, which triggers
	// GitHub's auto-submit after the 6th digit.
	if err := totpInput.PressSequentially(code, playwright.LocatorPressSequentiallyOptions{
		Delay: playwright.Float(50),
	}); err != nil {
		return false, fmt.Errorf("typing TOTP code: %w", err)
	}

	// GitHub's 2FA form auto-submits when 6 digits are entered.
	// Wait for the page to navigate away.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return false, fmt.Errorf("waiting for post-TOTP navigation: %w", err)
	}

	logf("[totp] TOTP submission succeeded (URL: %s)", page.URL())
	return true, nil
}

// waitForPageToLeave waits for the page to navigate away from a page whose
// title contains any of the given substrings.
func waitForPageToLeave(page playwright.Page, titleSubstrings ...string) error {
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for navigation: %w", err)
	}

	newTitle, _ := page.Title()
	for _, sub := range titleSubstrings {
		if strings.Contains(newTitle, sub) {
			return fmt.Errorf("still on page (title: %s)", newTitle)
		}
	}
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
