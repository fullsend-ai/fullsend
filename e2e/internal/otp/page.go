package otp

import (
	"fmt"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// EnterTOTPCode detects a GitHub TOTP input (#app_totp) on the current page,
// generates a code from the given secret, and enters it via simulated
// keystrokes. On the 2FA login page, GitHub auto-submits after 6 digits;
// on sudo pages, the submit button is clicked explicitly. If the first code
// is rejected, it retries once after waiting for the next TOTP period.
// Returns true if a TOTP form was found and submitted successfully.
// Returns (false, nil) if no TOTP input is visible.
func EnterTOTPCode(page playwright.Page, secret string, logf func(string, ...any)) (bool, error) {
	totpInput := page.Locator("#app_totp")
	if err := totpInput.WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(3000),
	}); err != nil {
		return false, nil
	}

	logf("[totp] Detected TOTP input on page (URL: %s)", page.URL())

	for attempt := 1; attempt <= 2; attempt++ {
		code, err := GenerateCode(secret)
		if err != nil {
			return false, fmt.Errorf("generating TOTP code: %w", err)
		}

		if attempt > 1 {
			// Clear the input before retrying.
			if err := totpInput.Fill(""); err != nil {
				return false, fmt.Errorf("clearing TOTP input for retry: %w", err)
			}
		}

		if err := totpInput.PressSequentially(code, playwright.LocatorPressSequentiallyOptions{
			Delay: playwright.Float(50),
		}); err != nil {
			return false, fmt.Errorf("typing TOTP code: %w", err)
		}

		// The 2FA login page auto-submits after 6 digits, but the sudo
		// confirmation page does not. Click submit if a button is visible.
		submitBtn := page.Locator("button[type='submit']")
		if visible, _ := submitBtn.First().IsVisible(); visible {
			logf("[totp] Clicking submit button (sudo page)")
			_ = submitBtn.First().Click(playwright.LocatorClickOptions{
				Timeout: playwright.Float(3000),
			})
		}

		if err := page.WaitForLoadState(playwright.PageWaitForLoadStateOptions{
			State: playwright.LoadStateDomcontentloaded,
		}); err != nil {
			return false, fmt.Errorf("waiting for post-TOTP navigation: %w", err)
		}

		// Verify we actually left the TOTP/sudo page.
		postURL := page.URL()
		postTitle, _ := page.Title()
		if !strings.Contains(postURL, "/two-factor") && !strings.Contains(postURL, "/2fa") &&
			!strings.Contains(postTitle, "Confirm access") && !strings.Contains(postTitle, "Sudo") {
			logf("[totp] TOTP submission succeeded (URL: %s)", postURL)
			return true, nil
		}

		if attempt == 1 {
			rem := time.Now().Second() % 30
			wait := time.Duration(30-rem+1) * time.Second
			logf("[totp] First TOTP code not accepted, waiting %s for next period", wait)
			time.Sleep(wait)
		}
	}

	postURL := page.URL()
	postTitle, _ := page.Title()
	return false, fmt.Errorf("TOTP code was not accepted after 2 attempts (still at %s, title: %s)", postURL, postTitle)
}
