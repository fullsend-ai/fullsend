//go:build e2e

package admin

import (
	"fmt"
	"strings"
	"time"

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
func createPAT(page playwright.Page, note string, logf func(string, ...any)) (string, error) {
	url := "https://github.com/settings/tokens/new"
	if _, err := page.Goto(url, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		logf("[pat] Current URL after navigation failure: %s", page.URL())
		return "", fmt.Errorf("navigating to token creation page: %w", err)
	}
	logf("[pat] Navigated to: %s", page.URL())

	// Verify we're on the right page.
	if err := page.Locator("#oauth_access_description").WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("token creation form not found (may not be logged in): %w", err)
	}

	// Fill in the token note/description.
	if err := page.Locator("#oauth_access_description").Fill(note); err != nil {
		return "", fmt.Errorf("filling token note: %w", err)
	}

	// Set expiration to 7 days.
	expirationSelect := page.Locator("#token_expiration")
	if _, err := expirationSelect.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice("seven_days"),
	}, playwright.LocatorSelectOptionOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
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

	logf("[pat] Created PAT: %s...%s (note: %s)", token[:4], token[len(token)-4:], note)
	return token, nil
}

// createDispatchPAT creates a fine-grained GitHub Personal Access Token
// scoped to the .fullsend repo with Actions read/write permission.
// This mirrors what the real CLI does in promptDispatchToken — the user
// is guided to create a fine-grained PAT at GitHub's token creation page.
// The e2e test automates the browser interaction instead.
//
// Prerequisites: the .fullsend repo must already exist (the config-repo
// and workflows layers must be installed first, just like the real CLI).
func createDispatchPAT(page playwright.Page, org, screenshotDir string, logf func(string, ...any)) (string, error) {
	// Navigate to the fine-grained PAT creation page.
	// Don't use target_name query param — GitHub's UI doesn't fully activate
	// the downstream widgets (repo picker, permissions) when pre-filled.
	// Instead, we'll select the owner manually.
	patURL := "https://github.com/settings/personal-access-tokens/new"

	logf("[dispatch-pat] Navigating to fine-grained PAT creation page")
	if _, err := page.Goto(patURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-goto-failed", logf)
		return "", fmt.Errorf("navigating to fine-grained PAT page: %w", err)
	}
	logf("[dispatch-pat] Page URL: %s", page.URL())

	// Wait for the form to render. The "Token name" label is a reliable signal.
	tokenNameLabel := page.Locator("text=Token name")
	if err := tokenNameLabel.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-form-not-loaded", logf)
		return "", fmt.Errorf("fine-grained PAT form did not load: %w", err)
	}
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-form-loaded", logf)

	// Fill in the token name using Playwright's label-based locator.
	tokenName := fmt.Sprintf("fullsend-dispatch-%s-e2e", org)
	nameInput := page.GetByLabel("Token name")
	if err := nameInput.Fill(tokenName); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-name-fill-failed", logf)
		return "", fmt.Errorf("filling token name: %w", err)
	}
	logf("[dispatch-pat] Filled token name: %s", tokenName)

	// Select the resource owner (org). The owner picker is a dropdown button
	// showing the current owner (e.g., "botsend ▼"). We need to click it
	// and select the org. Even if pre-filled, GitHub's UI may not activate
	// repo picker and permissions until the owner is manually interacted with.
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-before-owner", logf)

	// The resource owner is a custom dropdown button showing the current
	// owner (e.g., "botsend ▼"). Click it to open the owner picker.
	// Use JavaScript to find and click the owner button since it's a
	// custom React component.
	_, err := page.Evaluate(`() => {
		// Find all buttons/clickable elements near "Resource owner" text
		const labels = document.querySelectorAll('*');
		for (const el of labels) {
			if (el.textContent.trim() === 'Resource owner') {
				// The dropdown is the next interactive element after the label
				let sibling = el.nextElementSibling;
				while (sibling) {
					const btn = sibling.querySelector('button, summary, [role="button"]');
					if (btn) { btn.click(); return true; }
					if (sibling.tagName === 'BUTTON' || sibling.tagName === 'SUMMARY') {
						sibling.click(); return true;
					}
					sibling = sibling.nextElementSibling;
				}
			}
		}
		return false;
	}`)
	if err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-owner-btn-click", logf)
		return "", fmt.Errorf("clicking resource owner dropdown via JS: %w", err)
	}
	logf("[dispatch-pat] Clicked resource owner dropdown")
	time.Sleep(500 * time.Millisecond)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-owner-dropdown-open", logf)

	// Select the org from the dropdown.
	orgOption := page.Locator(fmt.Sprintf("[role='menuitemradio']:has-text('%s'), [role='option']:has-text('%s'), li:has-text('%s'), label:has-text('%s')", org, org, org, org))
	if err := orgOption.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-owner-option", logf)
		return "", fmt.Errorf("selecting org %s from owner dropdown: %w", org, err)
	}
	logf("[dispatch-pat] Selected resource owner: %s", org)

	// Wait for the page to update after owner selection — this may trigger
	// a re-render that adds the "Only select repositories" option and
	// repository permissions.
	time.Sleep(3 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-after-owner", logf)

	// Select "Only select repositories" radio button.
	selectReposLabel := page.Locator("label:has-text('Only select repositories')")
	if err := selectReposLabel.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		// Try the radio input directly.
		selectReposRadio := page.Locator("input[type='radio'][value='select']")
		if radioErr := selectReposRadio.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); radioErr != nil {
			saveDebugScreenshot(page, screenshotDir, "dispatch-pat-select-repos", logf)
			return "", fmt.Errorf("selecting 'Only select repositories': label=%w, radio=%v", err, radioErr)
		}
	}
	logf("[dispatch-pat] Selected 'Only select repositories'")

	// Wait for the repo picker to appear.
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-after-select-repos", logf)

	// Search for and select the .fullsend repo in the repo picker.
	repoSearch := page.Locator("input[type='text']")
	// The repo search is typically the last visible text input after the name input.
	// Let's find all visible text inputs and use the one that's for repo search.
	searchCount, _ := repoSearch.Count()
	logf("[dispatch-pat] Found %d text inputs on page", searchCount)

	// Try known selectors for the repo picker.
	repoPickerSelectors := []string{
		"input[placeholder*='Search for a repository']",
		"input[placeholder*='search']",
		"input[aria-label*='repository']",
		"input[aria-label*='repo']",
	}
	var foundRepoInput playwright.Locator
	for _, sel := range repoPickerSelectors {
		loc := page.Locator(sel)
		cnt, _ := loc.Count()
		if cnt > 0 {
			logf("[dispatch-pat] Found repo picker with selector: %s (count=%d)", sel, cnt)
			foundRepoInput = loc.First()
			break
		}
	}

	if foundRepoInput == nil {
		// Last resort: try clicking a "Select repositories" button/dropdown.
		selectRepoBtn := page.Locator("button:has-text('Select repositories'), summary:has-text('Select repositories')")
		if err := selectRepoBtn.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(3000),
		}); err != nil {
			saveDebugScreenshot(page, screenshotDir, "dispatch-pat-repo-picker-not-found", logf)
			return "", fmt.Errorf("could not find repo picker: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		// After clicking, look for a search input inside the dropdown.
		for _, sel := range repoPickerSelectors {
			loc := page.Locator(sel)
			cnt, _ := loc.Count()
			if cnt > 0 {
				foundRepoInput = loc.First()
				break
			}
		}
		if foundRepoInput == nil {
			// Try any text input that appeared.
			foundRepoInput = page.Locator("input[type='text']").Last()
		}
	}

	if err := foundRepoInput.Fill(".fullsend"); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-repo-search-fill", logf)
		return "", fmt.Errorf("typing .fullsend into repo search: %w", err)
	}
	logf("[dispatch-pat] Typed '.fullsend' into repo search")

	// Wait for the dropdown option and click it.
	time.Sleep(1 * time.Second)
	repoOption := page.Locator("[role='option']:has-text('.fullsend'), li:has-text('.fullsend'), label:has-text('.fullsend'), span:has-text('.fullsend')")
	if err := repoOption.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-repo-option-wait", logf)
		return "", fmt.Errorf("waiting for .fullsend repo option: %w", err)
	}
	if err := repoOption.First().Click(); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-repo-option-click", logf)
		return "", fmt.Errorf("selecting .fullsend repo: %w", err)
	}
	logf("[dispatch-pat] Selected .fullsend repository")

	// Close the repo picker popover. Press Escape multiple times to ensure
	// any open dropdown/popover is dismissed, then scroll the permissions
	// section into view.
	page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)
	page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)

	// Scroll down to make the permissions section and "Add permissions" visible.
	page.Locator("text=Permissions").Last().ScrollIntoViewIfNeeded()
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-after-close-picker", logf)

	// The permissions UI uses "+ Add permissions" button to open a dialog
	// where you can toggle individual permissions. Click it.
	addPermsBtn := page.Locator("button:has-text('Add permissions')")
	if err := addPermsBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-add-perms-btn", logf)
		return "", fmt.Errorf("clicking 'Add permissions': %w", err)
	}
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-perms-dialog", logf)

	// Find the Actions permission and set it to "Read and write".
	// The dialog shows a list of permissions with dropdowns.
	actionsRow := page.Locator("text=Actions").First()
	actionsSelect := actionsRow.Locator("xpath=ancestor::*[position()<=3]//select")
	actionsSelectCount, _ := actionsSelect.Count()
	if actionsSelectCount > 0 {
		if _, err := actionsSelect.First().SelectOption(playwright.SelectOptionValues{
			Values: playwright.StringSlice("write"),
		}); err != nil {
			// Try by label text.
			if _, err := actionsSelect.First().SelectOption(playwright.SelectOptionValues{
				Labels: playwright.StringSlice("Read and write"),
			}); err != nil {
				saveDebugScreenshot(page, screenshotDir, "dispatch-pat-actions-select", logf)
				return "", fmt.Errorf("selecting Actions write permission: %w", err)
			}
		}
	} else {
		// Maybe it's a toggle or checkbox — try clicking it.
		actionsToggle := page.Locator("[aria-label*='Actions'], label:has-text('Actions')").First()
		if err := actionsToggle.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(3000),
		}); err != nil {
			saveDebugScreenshot(page, screenshotDir, "dispatch-pat-actions-perm", logf)
			return "", fmt.Errorf("setting Actions permission: %w", err)
		}
	}
	logf("[dispatch-pat] Set Actions permission to Read and write")

	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-before-generate", logf)

	// Click "Generate token".
	generateBtn := page.Locator("button:has-text('Generate token')")
	if err := generateBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-generate-click", logf)
		return "", fmt.Errorf("clicking 'Generate token': %w", err)
	}
	logf("[dispatch-pat] Clicked 'Generate token'")

	// Wait for page to show the generated token.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return "", fmt.Errorf("waiting for token result page: %w", err)
	}
	time.Sleep(2 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-page", logf)

	// Fine-grained PATs display the token in a different element than classic PATs.
	tokenLocator := page.Locator("#new-oauth-token, [data-testid='new-token'], input[readonly][value^='github_pat_'], code:has-text('github_pat_'), div:has-text('github_pat_')")
	if err := tokenLocator.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-extract", logf)
		return "", fmt.Errorf("waiting for generated token element: %w", err)
	}

	// Try extracting the token value.
	token, err := tokenLocator.First().TextContent()
	if err != nil || token == "" {
		token, err = tokenLocator.First().InputValue()
		if err != nil || token == "" {
			saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-value", logf)
			return "", fmt.Errorf("extracting dispatch PAT value: %w", err)
		}
	}
	token = strings.TrimSpace(token)

	if token == "" {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-empty-token", logf)
		return "", fmt.Errorf("extracted dispatch PAT is empty")
	}

	logf("[dispatch-pat] Created fine-grained PAT: %s...%s", token[:10], token[len(token)-4:])
	return token, nil
}

// deleteDispatchPAT deletes a fine-grained GitHub PAT by navigating to the
// fine-grained tokens page and clicking delete for the matching token.
func deleteDispatchPAT(page playwright.Page, org, screenshotDir string, logf func(string, ...any)) error {
	tokenName := fmt.Sprintf("fullsend-dispatch-%s-e2e", org)

	if _, err := page.Goto("https://github.com/settings/personal-access-tokens", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		return fmt.Errorf("navigating to fine-grained tokens page: %w", err)
	}

	// Find the row containing our token name.
	tokenRow := page.Locator(fmt.Sprintf("a:has-text('%s')", tokenName)).Locator("xpath=ancestor::li | ancestor::div[contains(@class, 'list-group-item')]")
	if err := tokenRow.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
		State:   playwright.WaitForSelectorStateVisible,
	}); err != nil {
		logf("[dispatch-pat] Token %q not found on page, may already be deleted", tokenName)
		return nil
	}

	// Click the delete/revoke button.
	deleteBtn := tokenRow.First().Locator("button:has-text('Delete'), button:has-text('Revoke')")
	if err := deleteBtn.First().Click(); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-delete-click", logf)
		return fmt.Errorf("clicking delete for dispatch PAT %q: %w", tokenName, err)
	}

	// Wait for and click the confirmation button.
	confirmBtn := page.Locator("button:has-text('I understand'), button:has-text('Yes, revoke')")
	if err := confirmBtn.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-confirm-wait", logf)
		return fmt.Errorf("waiting for deletion confirmation for dispatch PAT: %w", err)
	}
	if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("confirming dispatch PAT deletion: %w", err)
	}

	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for dispatch PAT deletion to complete: %w", err)
	}

	logf("[dispatch-pat] Deleted fine-grained PAT: %s", tokenName)
	return nil
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
