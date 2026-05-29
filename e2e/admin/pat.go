//go:build e2e

package admin

import (
	"fmt"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// patScopes are the classic PAT scopes needed for e2e tests.
var patScopes = []string{
	"repo",
	"admin:org",
	"delete_repo",
	"workflow",
}

// createPAT creates a classic GitHub Personal Access Token via the browser.
// The token is created with a 7-day expiry and the scopes needed for e2e tests.
// Returns the token string.
func createPAT(page playwright.Page, note, password, totpSecret, screenshotDir string, logf func(string, ...any)) (string, error) {
	url := "https://github.com/settings/tokens/new"
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		logf("[pat] Current URL after navigation failure: %s", page.URL())
		return "", fmt.Errorf("navigating to token creation page: %w", err)
	}
	logf("[pat] Navigated to: %s", page.URL())

	// If we got redirected to login, the session isn't valid.
	if strings.Contains(page.URL(), "/login") {
		pageTitle, _ := page.Title()
		logf("[pat] ERROR: redirected to login page. Title: %s", pageTitle)
		return "", fmt.Errorf("redirected to login when accessing token page (URL: %s) — session is not authenticated", page.URL())
	}

	// Handle sudo confirmation if GitHub requires re-authentication.
	if handled, err := handleSudoIfPresent(page, password, totpSecret, screenshotDir, logf); err != nil {
		return "", fmt.Errorf("sudo confirmation for PAT creation: %w", err)
	} else if handled {
		// After sudo, we may need to re-navigate to the token page.
		if _, err := page.Goto(url, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(7500),
		}); err != nil {
			return "", fmt.Errorf("re-navigating to token page after sudo: %w", err)
		}
	}

	// Verify we're on the right page.
	if err := page.Locator("#oauth_access_description").WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		pageTitle, _ := page.Title()
		pageURL := page.URL()
		logf("[pat] ERROR: form not found. URL=%s Title=%s", pageURL, pageTitle)
		return "", fmt.Errorf("token creation form not found at %s (title: %s): %w", pageURL, pageTitle, err)
	}

	// Fill in the token note/description.
	if err := page.Locator("#oauth_access_description").Fill(note); err != nil {
		return "", fmt.Errorf("filling token note: %w", err)
	}

	// Set expiration to 7 days.
	// GitHub's classic PAT page may use different selectors across UI updates.
	// Try multiple selectors and interaction strategies for robustness.
	if err := setTokenExpiration(page, screenshotDir, logf); err != nil {
		logf("[pat] Warning: could not set expiration, using default: %v", err)
	}

	// Check the required scope checkboxes.
	for _, scope := range patScopes {
		checkbox := page.Locator(fmt.Sprintf("input[type='checkbox'][value='%s']", scope))
		if err := checkbox.Check(); err != nil {
			return "", fmt.Errorf("checking scope %s: %w", scope, err)
		}
	}

	// Click "Generate token".
	generateBtn := page.Locator("button:has-text('Generate token')")
	if err := generateBtn.Click(); err != nil {
		return "", fmt.Errorf("clicking Generate token: %w", err)
	}

	// Wait for the page to load with the new token displayed.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return "", fmt.Errorf("waiting for token page to load: %w", err)
	}

	// Extract the token value.
	tokenElement := page.Locator("#new-oauth-token")
	if err := tokenElement.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("token element not found on page: %w", err)
	}

	token, err := tokenElement.TextContent()
	if err != nil {
		return "", fmt.Errorf("extracting token text: %w", err)
	}

	if token == "" {
		return "", fmt.Errorf("extracted token is empty")
	}

	logf("[pat] Created PAT: %s**** (note: %s)", token[:4], note)
	return token, nil
}

// setTokenExpiration attempts to set the token expiration to 7 days on the
// classic PAT creation page. It tries multiple selectors and interaction
// strategies to handle GitHub UI changes gracefully.
func setTokenExpiration(page playwright.Page, screenshotDir string, logf func(string, ...any)) error {
	// Candidate selectors for the expiration <select> element, ordered by
	// likelihood. GitHub has changed the element ID across UI updates.
	selectors := []string{
		"#token_expiration",
		"select[name='token[expiration]']",
		"select[id*='expiration']",
		"select[name*='expiration']",
	}

	// Option values GitHub has used for 7-day expiration.
	optionValues := []string{"seven_days", "7"}

	for _, sel := range selectors {
		loc := page.Locator(sel)

		// Wait for the element to be attached to the DOM before interacting.
		if err := loc.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateAttached,
			Timeout: playwright.Float(3000),
		}); err != nil {
			logf("[pat] Selector %q not found, trying next", sel)
			continue
		}

		// Try each known option value.
		for _, val := range optionValues {
			if _, err := loc.SelectOption(playwright.SelectOptionValues{
				Values: playwright.StringSlice(val),
			}, playwright.LocatorSelectOptionOptions{
				Timeout: playwright.Float(3000),
			}); err == nil {
				logf("[pat] Set expiration via %s = %s", sel, val)
				return nil
			}
		}

		// SelectOption failed for all values — try setting via JavaScript
		// in case the element is present but the option values differ.
		for _, val := range optionValues {
			result, err := page.Evaluate(
				`([sel, val]) => {
					const el = document.querySelector(sel);
					if (!el) return false;
					const opt = Array.from(el.options).find(o => o.value === val);
					if (!opt) return false;
					el.value = val;
					el.dispatchEvent(new Event('change', {bubbles: true}));
					return true;
				}`,
				[]string{sel, val},
			)
			if err == nil && result == true {
				logf("[pat] Set expiration via JS: %s = %s", sel, val)
				return nil
			}
		}

		logf("[pat] Found %q but could not set a 7-day value", sel)
	}

	saveDebugScreenshot(page, screenshotDir, "pat-expiration-failed", logf)
	return fmt.Errorf("no expiration selector matched on the PAT creation page")
}

// deletePAT deletes a classic GitHub PAT by navigating to the tokens page
// and clicking delete for the token matching the given note.
func deletePAT(page playwright.Page, note string, logf func(string, ...any)) error {
	if _, err := page.Goto("https://github.com/settings/tokens", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		return fmt.Errorf("navigating to tokens page: %w", err)
	}

	// Find the row containing our token note and click its delete button.
	tokenRow := page.Locator(fmt.Sprintf("a:has-text('%s')", note)).Locator("xpath=ancestor::div[contains(@class, 'list-group-item')]")

	// Wait for the token row to appear.
	if err := tokenRow.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
		State:   playwright.WaitForSelectorStateVisible,
	}); err != nil {
		logf("[pat] Token %q not found on page, may already be deleted", note)
		return nil
	}

	deleteBtn := tokenRow.Locator("button:has-text('Delete')")
	if err := deleteBtn.Click(); err != nil {
		return fmt.Errorf("clicking delete for token %q: %w", note, err)
	}

	// Wait for confirmation button in the modal.
	confirmBtn := page.Locator("button:has-text('I understand, delete this token')")
	if err := confirmBtn.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("waiting for deletion confirmation for %q: %w", note, err)
	}
	if err := confirmBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("confirming token deletion for %q: %w", note, err)
	}

	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for deletion to complete: %w", err)
	}

	logf("[pat] Deleted PAT: %s", note)
	return nil
}
