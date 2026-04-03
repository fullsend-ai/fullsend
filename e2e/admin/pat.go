//go:build e2e

package admin

import (
	"fmt"
	"time"

	"github.com/playwright-community/playwright-go"
)

// patScopes are the classic PAT scopes needed for e2e tests.
var patScopes = []string{
	"repo",
	"admin:org",
	"delete_repo",
}

// createPAT creates a classic GitHub Personal Access Token via the browser.
// The token is created with a 1-hour expiry and the scopes needed for e2e tests.
// Returns the token string.
func createPAT(page playwright.Page, note string) (string, error) {
	url := "https://github.com/settings/tokens/new"
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return "", fmt.Errorf("navigating to token creation page: %w", err)
	}

	// Verify we're on the right page.
	if err := page.Locator("#oauth_access_description").WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return "", fmt.Errorf("token creation form not found (may not be logged in): %w", err)
	}

	// Fill in the token note/description.
	if err := page.Locator("#oauth_access_description").Fill(note); err != nil {
		return "", fmt.Errorf("filling token note: %w", err)
	}

	// Set expiration to shortest option (custom 1 day is fine for a test run).
	// The expiration dropdown defaults to 30 days. Select "7 days" for safety.
	expirationSelect := page.Locator("#token_expiration")
	if _, err := expirationSelect.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice("seven_days"),
	}); err != nil {
		// If the select option doesn't work with "seven_days", try other values.
		// GitHub's option values may vary; fall back to leaving the default.
		fmt.Printf("[pat] Warning: could not set expiration, using default: %v\n", err)
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
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return "", fmt.Errorf("waiting for token page to load: %w", err)
	}

	// Extract the token value. GitHub displays it in a code element with id "new-oauth-token".
	tokenElement := page.Locator("#new-oauth-token")
	if err := tokenElement.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(10000),
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

	fmt.Printf("[pat] Created PAT: %s...%s (note: %s)\n", token[:4], token[len(token)-4:], note)
	return token, nil
}

// deletePAT deletes a classic GitHub PAT by navigating to the tokens page
// and clicking delete for the token matching the given note.
func deletePAT(page playwright.Page, note string) error {
	if _, err := page.Goto("https://github.com/settings/tokens", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("navigating to tokens page: %w", err)
	}

	// Find the row containing our token note and click its delete button.
	// Each token is in a list-group-item with the description as a link.
	tokenRow := page.Locator(fmt.Sprintf("a:has-text('%s')", note)).Locator("xpath=ancestor::div[contains(@class, 'list-group-item')]")

	// Wait briefly — it might not exist if creation failed.
	if err := tokenRow.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
		State:   playwright.WaitForSelectorStateVisible,
	}); err != nil {
		fmt.Printf("[pat] Token %q not found on page, may already be deleted\n", note)
		return nil
	}

	deleteBtn := tokenRow.Locator("button:has-text('Delete')")
	if err := deleteBtn.Click(); err != nil {
		return fmt.Errorf("clicking delete for token %q: %w", note, err)
	}

	// Confirm deletion in the modal.
	// Give it a moment to appear.
	time.Sleep(500 * time.Millisecond)
	confirmBtn := page.Locator("button:has-text('I understand, delete this token')")
	if err := confirmBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return fmt.Errorf("confirming token deletion for %q: %w", note, err)
	}

	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		return fmt.Errorf("waiting for deletion to complete: %w", err)
	}

	fmt.Printf("[pat] Deleted PAT: %s\n", note)
	return nil
}
