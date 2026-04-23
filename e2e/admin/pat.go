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
func createPAT(page playwright.Page, note, password, screenshotDir string, logf func(string, ...any)) (string, error) {
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
	if handled, err := handleSudoIfPresent(page, password, screenshotDir, logf); err != nil {
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

	logf("[pat] Created PAT: %s**** (note: %s)", token[:4], note)
	return token, nil
}

// createDispatchPAT creates a fine-grained GitHub Personal Access Token
// scoped to the .fullsend repo with Actions read/write and Contents read permissions.
// This mirrors what the real CLI does in promptDispatchToken — the user
// is guided to create a fine-grained PAT at GitHub's token creation page.
// The e2e test automates the browser interaction instead.
//
// Prerequisites: the .fullsend repo must already exist (the config-repo
// and workflows layers must be installed first, just like the real CLI).
func createDispatchPAT(page playwright.Page, org, password, screenshotDir string, logf func(string, ...any)) (string, error) {
	// Navigate to the fine-grained PAT creation page.
	// Don't use target_name query param — GitHub's UI doesn't fully activate
	// the downstream widgets (repo picker, permissions) when pre-filled.
	// Instead, we'll select the owner manually.
	patURL := "https://github.com/settings/personal-access-tokens/new"

	// First, navigate to the GitHub profile settings to refresh the session.
	// After the app creation/installation flow, the CSRF token may be stale,
	// which causes the token generation to silently fail with a "signed in
	// with another tab" flash message.
	logf("[dispatch-pat] Refreshing session before PAT creation")
	if _, err := page.Goto("https://github.com/settings/profile", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		logf("[dispatch-pat] Warning: could not refresh session: %v", err)
	}
	time.Sleep(1 * time.Second)

	logf("[dispatch-pat] Navigating to fine-grained PAT creation page")
	if _, err := page.Goto(patURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-goto-failed", logf)
		return "", fmt.Errorf("navigating to fine-grained PAT page: %w", err)
	}
	logf("[dispatch-pat] Page URL: %s", page.URL())

	// Handle sudo confirmation if GitHub requires re-authentication.
	if handled, err := handleSudoIfPresent(page, password, screenshotDir, logf); err != nil {
		return "", fmt.Errorf("sudo confirmation for dispatch PAT creation: %w", err)
	} else if handled {
		// After sudo, re-navigate to the fine-grained PAT page.
		if _, err := page.Goto(patURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(15000),
		}); err != nil {
			return "", fmt.Errorf("re-navigating to fine-grained PAT page after sudo: %w", err)
		}
	}

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
	// Use a short timestamp to avoid name collisions (max 40 chars).
	tokenName := fmt.Sprintf("fs-dispatch-%s-%d", org, time.Now().Unix())
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

	// The permissions popover shows checkboxes. Click the Actions checkbox
	// using JavaScript since the checkbox UI may be a custom component.
	_, err = page.Evaluate(`() => {
		const items = document.querySelectorAll('*');
		for (const el of items) {
			if (el.textContent.trim() === 'Actions' && el.closest('[role="option"], label, li')) {
				el.closest('[role="option"], label, li').click();
				return true;
			}
		}
		// Fallback: find checkbox near "Actions" text
		for (const el of items) {
			if (el.textContent.trim() === 'Actions') {
				const parent = el.parentElement;
				const checkbox = parent.querySelector('input[type="checkbox"]');
				if (checkbox) { checkbox.click(); return true; }
				// Try clicking the parent itself
				parent.click();
				return true;
			}
		}
		return false;
	}`)
	if err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-actions-checkbox", logf)
		return "", fmt.Errorf("clicking Actions checkbox via JS: %w", err)
	}
	logf("[dispatch-pat] Checked Actions permission")
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-after-actions-check", logf)

	// Also select the Contents permission (defaults to Read-only, which is
	// what we need). This allows the dispatch token to resolve the default
	// branch on private .fullsend repos via GraphQL.
	_, err = page.Evaluate(`() => {
		const items = document.querySelectorAll('*');
		for (const el of items) {
			if (el.textContent.trim() === 'Contents' && el.closest('[role="option"], label, li')) {
				el.closest('[role="option"], label, li').click();
				return true;
			}
		}
		for (const el of items) {
			if (el.textContent.trim() === 'Contents') {
				const parent = el.parentElement;
				const checkbox = parent.querySelector('input[type="checkbox"]');
				if (checkbox) { checkbox.click(); return true; }
				parent.click();
				return true;
			}
		}
		return false;
	}`)
	if err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-contents-checkbox", logf)
		return "", fmt.Errorf("clicking Contents checkbox via JS: %w", err)
	}
	logf("[dispatch-pat] Checked Contents permission (Read-only)")
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-after-contents-check", logf)

	// Close the permissions popover by pressing Escape.
	page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)

	// The Actions permission is now added but defaults to "Read-only".
	// GitHub's fine-grained PAT UI uses custom React dropdowns (not native
	// <select> elements). Find the "Read-only" button/link near "Actions"
	// and click it to change the permission level.
	//
	// The permission row shows: "Actions" ... "Access: Read-only ▼"
	// Clicking "Read-only" opens a popover with "Read-only" and "Read and write" options.
	readOnlyBtn := page.Locator("button:has-text('Read-only')").First()
	if clickErr := readOnlyBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); clickErr != nil {
		logf("[dispatch-pat] Warning: could not click 'Read-only' button: %v", clickErr)
	} else {
		logf("[dispatch-pat] Clicked 'Read-only' dropdown")
		time.Sleep(500 * time.Millisecond)
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-rw-dropdown", logf)

		// Select "Read and write" from the opened dropdown/popover.
		rwOption := page.Locator("[role='option']:has-text('Read and write'), [role='menuitemradio']:has-text('Read and write'), li:has-text('Read and write'), label:has-text('Read and write')")
		if rwErr := rwOption.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(3000),
		}); rwErr != nil {
			logf("[dispatch-pat] Warning: could not click 'Read and write' option: %v", rwErr)
			// Fallback: try clicking any text that says "Read and write"
			rwText := page.Locator("text=Read and write")
			if textErr := rwText.First().Click(playwright.LocatorClickOptions{
				Timeout: playwright.Float(3000),
			}); textErr != nil {
				logf("[dispatch-pat] Warning: fallback text click also failed: %v", textErr)
			} else {
				logf("[dispatch-pat] Set Actions to Read and write via text click fallback")
			}
		} else {
			logf("[dispatch-pat] Set Actions to Read and write")
		}
	}
	time.Sleep(500 * time.Millisecond)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-before-generate", logf)

	// Click "Generate token" — this opens a confirmation dialog.
	generateBtn := page.Locator("button:has-text('Generate token')")
	if err := generateBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-generate-click", logf)
		return "", fmt.Errorf("clicking 'Generate token': %w", err)
	}
	logf("[dispatch-pat] Clicked first 'Generate token'")
	time.Sleep(1 * time.Second)

	// A confirmation dialog appears. Click the last "Generate token" button
	// which is the one inside the dialog (the main page also has one behind it).
	time.Sleep(1 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-confirm-dialog", logf)
	dialogGenerate := page.Locator("button:has-text('Generate token')").Last()
	if err := dialogGenerate.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-confirm-generate-failed", logf)
		return "", fmt.Errorf("clicking confirmation 'Generate token': %w", err)
	}
	logf("[dispatch-pat] Clicked confirmation 'Generate token'")

	// Wait for navigation to complete after token generation.
	if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		logf("[dispatch-pat] Warning: WaitForLoadState networkidle: %v", err)
	}
	time.Sleep(2 * time.Second)
	saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-page", logf)
	logf("[dispatch-pat] Token page URL: %s", page.URL())

	// Dump the full page HTML to help debug token extraction.
	if htmlContent, evalErr := page.Evaluate(`() => document.body.innerHTML`); evalErr == nil {
		htmlStr, _ := htmlContent.(string)
		// Look for github_pat_ in the raw HTML (it may be in an attribute
		// not found by our targeted queries).
		if idx := strings.Index(htmlStr, "github_pat_"); idx >= 0 {
			end := idx + 100
			if end > len(htmlStr) {
				end = len(htmlStr)
			}
			logf("[dispatch-pat] Found github_pat_ in HTML at offset %d", idx)
		} else {
			// Log a snippet of the page for debugging.
			snippet := htmlStr
			if len(snippet) > 500 {
				snippet = snippet[:500]
			}
			logf("[dispatch-pat] No github_pat_ found in HTML. First 500 chars: %s", snippet)
		}
	}

	// Try multiple strategies to extract the token value.
	tokenResult, err := page.Evaluate(`() => {
		// Strategy 1: Look for any input whose value starts with github_pat_
		const inputs = document.querySelectorAll('input');
		for (const input of inputs) {
			if (input.value && input.value.startsWith('github_pat_')) {
				return input.value;
			}
		}
		// Strategy 2: Check code/pre/span elements
		const codeEls = document.querySelectorAll('code, pre, span, div, [data-testid]');
		for (const el of codeEls) {
			const text = el.textContent || '';
			if (text.startsWith('github_pat_') && text.length > 30) {
				return text.trim();
			}
		}
		// Strategy 3: Check clipboard button data attributes
		const clipboardBtns = document.querySelectorAll('[data-clipboard-text], clipboard-copy, [value]');
		for (const btn of clipboardBtns) {
			const val = btn.getAttribute('data-clipboard-text') || btn.getAttribute('value') || '';
			if (val.startsWith('github_pat_')) {
				return val;
			}
		}
		// Strategy 4: Regex on full page text
		const allText = document.body.innerText;
		const match = allText.match(/github_pat_[A-Za-z0-9_]+/);
		if (match) return match[0];
		// Strategy 5: Search ALL attributes of ALL elements
		const allEls = document.querySelectorAll('*');
		for (const el of allEls) {
			for (const attr of el.attributes) {
				if (attr.value && attr.value.startsWith('github_pat_')) {
					return attr.value;
				}
			}
		}
		return null;
	}`)
	if err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-extract-failed", logf)
		return "", fmt.Errorf("extracting dispatch PAT via JS: %w", err)
	}

	token, ok := tokenResult.(string)
	if !ok || token == "" {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-token-empty", logf)
		logf("[dispatch-pat] Current URL: %s", page.URL())
		if errMsg, evalErr := page.Evaluate(`() => {
			const flash = document.querySelector('.flash-error, .flash-warn, [role="alert"]');
			return flash ? flash.textContent.trim() : '(no flash message)';
		}`); evalErr == nil {
			logf("[dispatch-pat] Flash message: %v", errMsg)
		}
		return "", fmt.Errorf("dispatch PAT value is empty or not a string: %v", tokenResult)
	}
	token = strings.TrimSpace(token)

	if token == "" {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-empty-token", logf)
		return "", fmt.Errorf("extracted dispatch PAT is empty")
	}

	logf("[dispatch-pat] Created fine-grained PAT: %s****", token[:11])
	return token, nil
}

// deleteDispatchPAT deletes a fine-grained GitHub PAT by navigating to the
// fine-grained tokens page and clicking delete for the matching token.
func deleteDispatchPAT(page playwright.Page, org, screenshotDir string, logf func(string, ...any)) error {
	// Match any token with the dispatch prefix (name includes timestamp).
	tokenPrefix := fmt.Sprintf("fs-dispatch-%s-", org)

	if _, err := page.Goto("https://github.com/settings/personal-access-tokens", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		return fmt.Errorf("navigating to fine-grained tokens page: %w", err)
	}

	// Find any row containing our token prefix.
	tokenRow := page.Locator(fmt.Sprintf("a:has-text('%s')", tokenPrefix)).Locator("xpath=ancestor::li | ancestor::div[contains(@class, 'list-group-item')]")
	if err := tokenRow.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
		State:   playwright.WaitForSelectorStateVisible,
	}); err != nil {
		logf("[dispatch-pat] Token with prefix %q not found on page, may already be deleted", tokenPrefix)
		return nil
	}

	// Click the delete/revoke button.
	deleteBtn := tokenRow.First().Locator("button:has-text('Delete'), button:has-text('Revoke')")
	if err := deleteBtn.First().Click(); err != nil {
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-delete-click", logf)
		return fmt.Errorf("clicking delete for dispatch PAT %q: %w", tokenPrefix, err)
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

	logf("[dispatch-pat] Deleted fine-grained PAT with prefix: %s", tokenPrefix)
	return nil
}

// deleteAllDispatchPATs deletes all fine-grained PATs from the account's
// token list page. This handles accumulated stale PATs that hit GitHub's
// 50-token limit. It deletes tokens indiscriminately because the e2e test
// account is dedicated and should not have non-test tokens.
func deleteAllDispatchPATs(page playwright.Page, _, screenshotDir string, logf func(string, ...any)) {
	for i := 0; i < 60; i++ { // safety limit
		// Re-navigate each iteration since deleting reloads the page.
		if _, err := page.Goto("https://github.com/settings/personal-access-tokens", playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(10000),
		}); err != nil {
			logf("[dispatch-pat-cleanup] Could not navigate to tokens page: %v", err)
			return
		}
		time.Sleep(1 * time.Second)
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-cleanup-list", logf)

		// Log visible token names for debugging.
		if names, evalErr := page.Evaluate(`() => {
			const links = document.querySelectorAll('a[href*="/settings/personal-access-tokens/"]');
			return Array.from(links).map(a => a.textContent.trim()).filter(t => t.length > 0).slice(0, 10);
		}`); evalErr == nil {
			logf("[dispatch-pat-cleanup] Visible tokens: %v", names)
		}

		// Click the first visible "Delete" on the page using Playwright's
		// native click (not JS) so that Turbo/Hotwire navigation works.
		deleteEl := page.GetByText("Delete", playwright.PageGetByTextOptions{Exact: playwright.Bool(true)}).First()
		if err := deleteEl.WaitFor(playwright.LocatorWaitForOptions{
			Timeout: playwright.Float(3000),
			State:   playwright.WaitForSelectorStateVisible,
		}); err != nil {
			logf("[dispatch-pat-cleanup] No more deletable tokens found (cleaned %d)", i)
			return
		}

		if err := deleteEl.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			logf("[dispatch-pat-cleanup] Could not click delete: %v", err)
			saveDebugScreenshot(page, screenshotDir, fmt.Sprintf("dispatch-pat-cleanup-delete-%d", i), logf)
			return
		}

		// Wait for confirmation page/dialog to load.
		time.Sleep(1 * time.Second)
		saveDebugScreenshot(page, screenshotDir, "dispatch-pat-cleanup-confirm", logf)

		// Click any confirmation button that appears.
		confirmBtn := page.GetByText("I understand, delete this token", playwright.PageGetByTextOptions{Exact: playwright.Bool(false)})
		if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); err != nil {
			// Try broader confirm selectors.
			altConfirm := page.Locator("button.btn-danger, input.btn-danger[type='submit']").First()
			if altErr := altConfirm.Click(playwright.LocatorClickOptions{
				Timeout: playwright.Float(3000),
			}); altErr != nil {
				logf("[dispatch-pat-cleanup] Could not confirm delete: %v / %v", err, altErr)
				saveDebugScreenshot(page, screenshotDir, fmt.Sprintf("dispatch-pat-cleanup-noconfirm-%d", i), logf)
				return
			}
		}

		_ = page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateDomcontentloaded,
		})
		logf("[dispatch-pat-cleanup] Deleted token %d", i+1)
	}
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
