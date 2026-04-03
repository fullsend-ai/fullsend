//go:build e2e

package admin

// AutoPrompter implements appsetup.Prompter for non-interactive e2e use.
// It automatically accepts all prompts without human input.
type AutoPrompter struct{}

// WaitForEnter returns immediately. In e2e tests, Playwright handles
// the browser interactions that the prompt would normally gate on.
func (AutoPrompter) WaitForEnter(_ string) error {
	return nil
}

// Confirm always returns true, accepting any confirmation prompt
// (e.g., reuse existing app).
func (AutoPrompter) Confirm(_ string) (bool, error) {
	return true, nil
}
