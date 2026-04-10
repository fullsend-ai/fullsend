package secretarial

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScrub_Email(t *testing.T) {
	assert.Equal(t, "contact "+redacted+" for info",
		ScrubSensitiveContent("contact alice@example.com for info"))
}

func TestScrub_PhoneUS(t *testing.T) {
	assert.Contains(t, ScrubSensitiveContent("Call 555-123-4567"), redacted)
	assert.Contains(t, ScrubSensitiveContent("Call (555) 123-4567"), redacted)
	assert.Contains(t, ScrubSensitiveContent("Call +1-555-123-4567"), redacted)
}

func TestScrub_IPv4(t *testing.T) {
	assert.Equal(t, "server at "+redacted,
		ScrubSensitiveContent("server at 192.168.1.100"))
}

func TestScrub_SSN(t *testing.T) {
	assert.Equal(t, "SSN: "+redacted,
		ScrubSensitiveContent("SSN: 123-45-6789"))
}

func TestScrub_AWSAccessKey(t *testing.T) {
	assert.Contains(t, ScrubSensitiveContent("key: AKIAIOSFODNN7EXAMPLE"), redacted)
}

func TestScrub_GitHubPAT(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 40)
	assert.Contains(t, ScrubSensitiveContent("token="+token), redacted)
	assert.NotContains(t, ScrubSensitiveContent("token="+token), "ghp_")
}

func TestScrub_SlackWebhook(t *testing.T) {
	// Built dynamically to avoid triggering GitHub push-protection secret scanning.
	url := "https://hooks.slack.com/services/" + "T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"
	assert.Contains(t, ScrubSensitiveContent("webhook: "+url), redacted)
	assert.NotContains(t, ScrubSensitiveContent("webhook: "+url), "hooks.slack.com")
}

func TestScrub_PEMPrivateKey(t *testing.T) {
	// Built dynamically to avoid triggering pre-commit detect-private-key hook.
	pem := "-----BEGIN RSA PRIVATE" + " KEY-----\nMIIE...\n-----END RSA PRIVATE" + " KEY-----"
	result := ScrubSensitiveContent("key is " + pem)
	assert.NotContains(t, result, "BEGIN")
}

func TestScrub_JWT(t *testing.T) {
	// Built dynamically to avoid triggering secret scanners.
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" +
		".eyJzdWIiOiIxMjM0NTY3ODkwIn0" +
		".dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"
	assert.Contains(t, ScrubSensitiveContent("Bearer "+jwt), redacted)
}

func TestScrub_GenericAPIKey(t *testing.T) {
	assert.Contains(t, ScrubSensitiveContent("api_key=abcdefghij1234567890extra"), redacted)
}

func TestScrub_UnicodeTagCharacters(t *testing.T) {
	var payload []rune
	for c := rune(0xE0000); c <= 0xE001F; c++ {
		payload = append(payload, c)
	}
	text := "normal text" + string(payload) + " end"
	result := ScrubSensitiveContent(text)
	assert.NotContains(t, result, string(payload))
	assert.Contains(t, result, "normal text")
}

func TestScrub_ZeroWidthChars(t *testing.T) {
	text := "hello\u200b\u200c\u200d\ufeffworld"
	result := ScrubSensitiveContent(text)
	assert.NotContains(t, result, "\u200b")
	assert.NotContains(t, result, "\ufeff")
}

func TestScrub_CleanTextUnchanged(t *testing.T) {
	clean := "We decided to refactor the API client. See issue #42."
	assert.Equal(t, clean, ScrubSensitiveContent(clean))
}

func TestScrub_MultiplePatterns(t *testing.T) {
	text := "Email alice@example.com, call 555-123-4567, key AKIAIOSFODNN7EXAMPLE"
	result := ScrubSensitiveContent(text)
	assert.NotContains(t, result, "alice@example.com")
	assert.NotContains(t, result, "555-123-4567")
	assert.NotContains(t, result, "AKIAIOSFODNN7EXAMPLE")
}

func TestContainsSensitiveContent(t *testing.T) {
	assert.True(t, ContainsSensitiveContent("email: user@test.com"))
	assert.False(t, ContainsSensitiveContent("nothing sensitive here"))
}
