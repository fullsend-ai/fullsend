// cleanup-pats navigates to the GitHub classic PAT settings page via Playwright
// and deletes all expired tokens. Iterates through paginated token pages from
// last to first, deleting expired tokens on each page. Reports how many
// unexpired tokens remain and prints the URL for manual review.
//
// Usage: go run hack/cleanup-pats.go
//
//go:build ignore

package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/playwright-community/playwright-go"
)

const tokensURL = "https://github.com/settings/tokens"

func main() {
	sessionFile := os.Getenv("E2E_GITHUB_SESSION_FILE")
	if sessionFile == "" {
		log.Fatal("Set E2E_GITHUB_SESSION_FILE to a Playwright storageState JSON file")
	}
	if _, err := os.Stat(sessionFile); err != nil {
		log.Fatalf("E2E_GITHUB_SESSION_FILE %q does not exist: %v", sessionFile, err)
	}

	pw, err := playwright.Run()
	if err != nil {
		log.Fatalf("starting playwright: %v", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		log.Fatalf("launching browser: %v", err)
	}
	defer browser.Close()

	ctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		StorageStatePath: playwright.String(sessionFile),
	})
	if err != nil {
		log.Fatalf("creating context: %v", err)
	}

	page, err := ctx.NewPage()
	if err != nil {
		log.Fatalf("creating page: %v", err)
	}

	// Load first page to get total page count.
	if _, err := page.Goto(tokensURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		log.Fatalf("navigating to tokens page: %v", err)
	}

	if strings.Contains(page.URL(), "/login") {
		log.Fatalf("session is not authenticated — redirected to %s", page.URL())
	}

	totalPages := 1
	currentEl := page.Locator(".current[data-total-pages]")
	if c, _ := currentEl.Count(); c > 0 {
		if tp, err := currentEl.GetAttribute("data-total-pages"); err == nil {
			if n, err := strconv.Atoi(tp); err == nil {
				totalPages = n
			}
		}
	}
	fmt.Printf("Token pages: %d\n", totalPages)

	deleted := 0
	// Work backwards from the last page where expired tokens live.
	for pg := totalPages; pg >= 1; pg-- {
		pageURL := fmt.Sprintf("%s?page=%d", tokensURL, pg)
		if _, err := page.Goto(pageURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(15000),
		}); err != nil {
			log.Printf("could not load page %d: %v, stopping", pg, err)
			break
		}

		pageDeleted := deleteExpiredOnPage(page, pg)
		deleted += pageDeleted

		if pageDeleted == 0 {
			fmt.Printf("Page %d: no expired tokens.\n", pg)
		}
	}

	// Navigate back to page 1 to count active tokens.
	if _, err := page.Goto(tokensURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(15000),
	}); err != nil {
		log.Printf("could not reload first page: %v", err)
	}

	// Re-read total pages for the remaining count.
	remainingPages := 1
	currentEl = page.Locator(".current[data-total-pages]")
	if c, _ := currentEl.Count(); c > 0 {
		if tp, err := currentEl.GetAttribute("data-total-pages"); err == nil {
			if n, err := strconv.Atoi(tp); err == nil {
				remainingPages = n
			}
		}
	}
	firstPageCount, _ := page.Locator(".access-token").Count()

	// Estimate: full pages have 10 tokens, last page has firstPageCount.
	var remaining int
	if remainingPages == 1 {
		remaining = firstPageCount
	} else {
		remaining = (remainingPages-1)*10 + firstPageCount
	}

	fmt.Println()
	fmt.Printf("Deleted:   %d expired PATs\n", deleted)
	fmt.Printf("Remaining: ~%d unexpired PATs\n", remaining)
	fmt.Printf("\nReview remaining tokens at: %s\n", tokensURL)
}

// deleteExpiredOnPage deletes all expired tokens visible on the current page.
// Returns the number deleted.
func deleteExpiredOnPage(page playwright.Page, pg int) int {
	const maxDeletes = 100 // guard against infinite loops
	deleted := 0
	for deleted < maxDeletes {
		// Find tokens with "Expired on" text on this page.
		expiredRows := page.Locator(".access-token:has-text('Expired on')")
		count, err := expiredRows.Count()
		if err != nil || count == 0 {
			break
		}

		row := expiredRows.First()
		text, _ := row.InnerText()
		// Extract the token name from the text (format: "Delete\n...\nname — scopes\nExpired on ...").
		name := extractTokenName(text)

		// Extract the form action URL and CSRF token, then POST
		// directly via page.Evaluate+fetch. This avoids fighting with
		// Playwright's navigation handling around form.submit().
		formAction, _ := row.Locator("form.js-revoke-access-form").GetAttribute("action")
		csrfToken, _ := row.Locator("form.js-revoke-access-form input[name='authenticity_token']").GetAttribute("value")
		if formAction == "" || csrfToken == "" {
			log.Printf("page %d: missing form action or CSRF token for %q, stopping", pg, name)
			break
		}

		// POST the delete and check the response status.
		js := fmt.Sprintf(`async () => {
			const resp = await fetch(%q, {
				method: 'POST',
				headers: {'Content-Type': 'application/x-www-form-urlencoded'},
				body: '_method=delete&authenticity_token=' + encodeURIComponent(%q),
			});
			if (!resp.ok) throw new Error('HTTP ' + resp.status);
		}`, formAction, csrfToken)
		if _, err := page.Evaluate(js); err != nil {
			log.Printf("page %d: delete fetch failed for %q: %v, stopping", pg, name, err)
			break
		}

		// Reload the page to see updated token list.
		if _, err := page.Reload(playwright.PageReloadOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(10000),
		}); err != nil {
			log.Printf("page %d: reload after deleting %q failed: %v, stopping", pg, name, err)
			deleted++
			break
		}

		deleted++
		fmt.Printf("  Page %d: deleted %s\n", pg, name)
	}

	if deleted > 0 {
		fmt.Printf("Page %d: deleted %d expired PATs\n", pg, deleted)
	}
	return deleted
}

// extractTokenName pulls the token note from the row's inner text.
// The text format is "Delete\n...\nNAME — scopes\nExpired on ...".
func extractTokenName(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "fullsend-e2e-") || strings.Contains(line, " — ") {
			if idx := strings.Index(line, " — "); idx > 0 {
				return line[:idx]
			}
			return line
		}
	}
	return "(unknown)"
}
