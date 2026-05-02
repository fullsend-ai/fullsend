package appsetup

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// CreateDispatchPAT creates a fine-grained GitHub PAT scoped to the .fullsend
// repo with Actions read/write and Contents read permissions. Returns the token.
// Ported from e2e/admin/pat.go createDispatchPAT.
func (b *PlaywrightBrowserOpener) CreateDispatchPAT(_ context.Context, org string) (string, error) {
	patURL := "https://github.com/settings/personal-access-tokens/new"

	// Refresh session before PAT creation to avoid stale CSRF tokens.
	b.ui.StepStart("Creating dispatch token via Playwright")
	if _, err := b.page.Goto("https://github.com/settings/profile", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(10000),
	}); err != nil {
		b.ui.StepWarn(fmt.Sprintf("Could not refresh session: %v", err))
	}
	time.Sleep(1 * time.Second)

	if _, err := b.page.Goto(patURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		return "", fmt.Errorf("navigating to fine-grained PAT page: %w", err)
	}

	if strings.Contains(b.page.URL(), "/login") {
		return "", fmt.Errorf("redirected to login — session is not authenticated")
	}

	// Wait for the form to render.
	tokenNameLabel := b.page.Locator("text=Token name")
	if err := tokenNameLabel.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	}); err != nil {
		return "", fmt.Errorf("fine-grained PAT form did not load: %w", err)
	}

	// Fill in the token name.
	tokenName := fmt.Sprintf("fs-dispatch-%s-%d", org, time.Now().Unix())
	nameInput := b.page.GetByLabel("Token name")
	if err := nameInput.Fill(tokenName); err != nil {
		return "", fmt.Errorf("filling token name: %w", err)
	}
	b.ui.StepInfo(fmt.Sprintf("Token name: %s", tokenName))

	// Select the resource owner (org).
	_, err := b.page.Evaluate(`() => {
		const labels = document.querySelectorAll('*');
		for (const el of labels) {
			if (el.textContent.trim() === 'Resource owner') {
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
		return "", fmt.Errorf("clicking resource owner dropdown: %w", err)
	}
	time.Sleep(500 * time.Millisecond)

	// Select the org from the dropdown.
	orgOption := b.page.Locator(fmt.Sprintf(
		"[role='menuitemradio']:has-text('%s'), [role='option']:has-text('%s'), li:has-text('%s'), label:has-text('%s')",
		org, org, org, org,
	))
	if err := orgOption.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("selecting org %s from owner dropdown: %w", org, err)
	}
	b.ui.StepInfo(fmt.Sprintf("Selected resource owner: %s", org))
	time.Sleep(3 * time.Second)

	// Select "Only select repositories".
	selectReposLabel := b.page.Locator("label:has-text('Only select repositories')")
	if err := selectReposLabel.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		selectReposRadio := b.page.Locator("input[type='radio'][value='select']")
		if radioErr := selectReposRadio.Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(5000),
		}); radioErr != nil {
			return "", fmt.Errorf("selecting 'Only select repositories': %w", err)
		}
	}
	time.Sleep(1 * time.Second)

	// Search for and select the .fullsend repo.
	repoPickerSelectors := []string{
		"input[placeholder*='Search for a repository']",
		"input[placeholder*='search']",
		"input[aria-label*='repository']",
		"input[aria-label*='repo']",
	}
	var foundRepoInput playwright.Locator
	for _, sel := range repoPickerSelectors {
		loc := b.page.Locator(sel)
		cnt, _ := loc.Count()
		if cnt > 0 {
			foundRepoInput = loc.First()
			break
		}
	}

	if foundRepoInput == nil {
		// Try clicking a "Select repositories" button/dropdown.
		selectRepoBtn := b.page.Locator("button:has-text('Select repositories'), summary:has-text('Select repositories')")
		if err := selectRepoBtn.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(3000),
		}); err != nil {
			return "", fmt.Errorf("could not find repo picker: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
		for _, sel := range repoPickerSelectors {
			loc := b.page.Locator(sel)
			cnt, _ := loc.Count()
			if cnt > 0 {
				foundRepoInput = loc.First()
				break
			}
		}
		if foundRepoInput == nil {
			foundRepoInput = b.page.Locator("input[type='text']").Last()
		}
	}

	if err := foundRepoInput.Fill(".fullsend"); err != nil {
		return "", fmt.Errorf("typing .fullsend into repo search: %w", err)
	}
	time.Sleep(1 * time.Second)

	repoOption := b.page.Locator("[role='option']:has-text('.fullsend'), li:has-text('.fullsend'), label:has-text('.fullsend'), span:has-text('.fullsend')")
	if err := repoOption.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("waiting for .fullsend repo option: %w", err)
	}
	if err := repoOption.First().Click(); err != nil {
		return "", fmt.Errorf("selecting .fullsend repo: %w", err)
	}

	// Close the repo picker popover.
	b.page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)
	b.page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)

	// Scroll to permissions and click "Add permissions".
	b.page.Locator("text=Permissions").Last().ScrollIntoViewIfNeeded()
	time.Sleep(1 * time.Second)

	addPermsBtn := b.page.Locator("button:has-text('Add permissions')")
	if err := addPermsBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("clicking 'Add permissions': %w", err)
	}
	time.Sleep(1 * time.Second)

	// Check Actions permission.
	_, err = b.page.Evaluate(`() => {
		const items = document.querySelectorAll('*');
		for (const el of items) {
			if (el.textContent.trim() === 'Actions' && el.closest('[role="option"], label, li')) {
				el.closest('[role="option"], label, li').click();
				return true;
			}
		}
		for (const el of items) {
			if (el.textContent.trim() === 'Actions') {
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
		return "", fmt.Errorf("clicking Actions checkbox: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Check Contents permission.
	_, err = b.page.Evaluate(`() => {
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
		return "", fmt.Errorf("clicking Contents checkbox: %w", err)
	}
	time.Sleep(1 * time.Second)

	// Close permissions popover.
	b.page.Keyboard().Press("Escape")
	time.Sleep(500 * time.Millisecond)

	// Change Actions from "Read-only" to "Read and write".
	readOnlyBtn := b.page.Locator("button:has-text('Read-only')").First()
	if clickErr := readOnlyBtn.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); clickErr != nil {
		b.ui.StepWarn(fmt.Sprintf("Could not click 'Read-only' button: %v", clickErr))
	} else {
		time.Sleep(500 * time.Millisecond)
		rwOption := b.page.Locator("[role='option']:has-text('Read and write'), [role='menuitemradio']:has-text('Read and write'), li:has-text('Read and write'), label:has-text('Read and write')")
		if rwErr := rwOption.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(3000),
		}); rwErr != nil {
			rwText := b.page.Locator("text=Read and write")
			if textErr := rwText.First().Click(playwright.LocatorClickOptions{
				Timeout: playwright.Float(3000),
			}); textErr != nil {
				b.ui.StepWarn(fmt.Sprintf("Could not set Actions to Read and write: %v", textErr))
			}
		}
	}
	time.Sleep(500 * time.Millisecond)

	// Click "Generate token" — opens a confirmation dialog.
	generateBtn := b.page.Locator("button:has-text('Generate token')")
	if err := generateBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("clicking 'Generate token': %w", err)
	}
	time.Sleep(2 * time.Second)

	// Click the confirmation "Generate token" button in the dialog.
	dialogGenerate := b.page.Locator("button:has-text('Generate token')").Last()
	if err := dialogGenerate.Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return "", fmt.Errorf("clicking confirmation 'Generate token': %w", err)
	}

	// Wait for navigation to complete.
	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateNetworkidle,
	}); err != nil {
		b.ui.StepWarn(fmt.Sprintf("WaitForLoadState networkidle: %v", err))
	}
	time.Sleep(2 * time.Second)

	// Extract the token value using multiple strategies.
	tokenResult, err := b.page.Evaluate(`() => {
		// Strategy 1: input values
		const inputs = document.querySelectorAll('input');
		for (const input of inputs) {
			if (input.value && input.value.startsWith('github_pat_')) {
				return input.value;
			}
		}
		// Strategy 2: code/pre/span elements
		const codeEls = document.querySelectorAll('code, pre, span, div, [data-testid]');
		for (const el of codeEls) {
			const text = el.textContent || '';
			if (text.startsWith('github_pat_') && text.length > 30) {
				return text.trim();
			}
		}
		// Strategy 3: clipboard button data attributes
		const clipboardBtns = document.querySelectorAll('[data-clipboard-text], clipboard-copy, [value]');
		for (const btn of clipboardBtns) {
			const val = btn.getAttribute('data-clipboard-text') || btn.getAttribute('value') || '';
			if (val.startsWith('github_pat_')) {
				return val;
			}
		}
		// Strategy 4: regex on full page text
		const allText = document.body.innerText;
		const match = allText.match(/github_pat_[A-Za-z0-9_]+/);
		if (match) return match[0];
		// Strategy 5: search ALL attributes
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
		return "", fmt.Errorf("extracting dispatch PAT: %w", err)
	}

	token, ok := tokenResult.(string)
	if !ok || token == "" {
		// Save debug screenshot.
		if _, sErr := b.page.Screenshot(playwright.PageScreenshotOptions{
			Path: playwright.String("/tmp/fullsend-pat-debug.png"),
		}); sErr == nil {
			b.ui.StepInfo("Debug screenshot saved to /tmp/fullsend-pat-debug.png")
		}
		return "", fmt.Errorf("dispatch PAT value is empty or not found")
	}
	token = strings.TrimSpace(token)

	b.ui.StepDone(fmt.Sprintf("Created dispatch token: %s****", token[:11]))
	return token, nil
}

// DeleteDispatchPAT deletes fine-grained PATs matching the dispatch naming
// convention for the given org.
func (b *PlaywrightBrowserOpener) DeleteDispatchPAT(_ context.Context, org string) error {
	tokenPrefix := fmt.Sprintf("fs-dispatch-%s-", org)
	b.ui.StepStart(fmt.Sprintf("Deleting dispatch PATs matching %s*", tokenPrefix))

	if _, err := b.page.Goto("https://github.com/settings/personal-access-tokens", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(7500),
	}); err != nil {
		return fmt.Errorf("navigating to fine-grained tokens page: %w", err)
	}

	// Find any row containing our token prefix.
	tokenRow := b.page.Locator(fmt.Sprintf("a:has-text('%s')", tokenPrefix)).Locator("xpath=ancestor::li | ancestor::div[contains(@class, 'list-group-item')]")
	if err := tokenRow.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(5000),
		State:   playwright.WaitForSelectorStateVisible,
	}); err != nil {
		b.ui.StepInfo(fmt.Sprintf("No dispatch PAT with prefix %q found, may already be deleted", tokenPrefix))
		return nil
	}

	// Click the delete/revoke button.
	deleteBtn := tokenRow.First().Locator("button:has-text('Delete'), button:has-text('Revoke')")
	if err := deleteBtn.First().Click(); err != nil {
		return fmt.Errorf("clicking delete for dispatch PAT: %w", err)
	}

	// Wait for and click the confirmation button.
	confirmBtn := b.page.Locator("button:has-text('I understand'), button:has-text('Yes, revoke')")
	if err := confirmBtn.First().WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("waiting for deletion confirmation: %w", err)
	}
	if err := confirmBtn.First().Click(playwright.LocatorClickOptions{
		Timeout: playwright.Float(5000),
	}); err != nil {
		return fmt.Errorf("confirming dispatch PAT deletion: %w", err)
	}

	if err := b.page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
		State: playwright.LoadStateDomcontentloaded,
	}); err != nil {
		return fmt.Errorf("waiting for PAT deletion to complete: %w", err)
	}

	b.ui.StepDone("Deleted dispatch PAT")
	return nil
}
