package ui

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBanner(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.Banner()
	out := buf.String()
	assert.Contains(t, out, "fullsend")
}

func TestHeader(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.Header("Install Components")
	out := buf.String()
	assert.Contains(t, out, "Install Components")
}

func TestStepStart(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.StepStart("checking repository")
	out := buf.String()
	assert.Contains(t, out, "\u2022")
	assert.Contains(t, out, "checking repository")
}

func TestStepDone(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.StepDone("repository configured")
	out := buf.String()
	assert.Contains(t, out, "\u2713")
	assert.Contains(t, out, "repository configured")
}

func TestStepFail(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.StepFail("permission denied")
	out := buf.String()
	assert.Contains(t, out, "\u2717")
	assert.Contains(t, out, "permission denied")
}

func TestStepWarn(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.StepWarn("token expires soon")
	out := buf.String()
	assert.Contains(t, out, "!")
	assert.Contains(t, out, "token expires soon")
}

func TestStepInfo(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.StepInfo("additional context here")
	out := buf.String()
	assert.Contains(t, out, "additional context here")
}

func TestKeyValue(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.KeyValue("org", "fullsend-ai")
	out := buf.String()
	assert.Contains(t, out, "org")
	assert.Contains(t, out, "fullsend-ai")
}

func TestSummary(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.Summary("Actions Taken", []string{"created repo", "set permissions", "enabled checks"})
	out := buf.String()
	assert.Contains(t, out, "Actions Taken")
	assert.Contains(t, out, "created repo")
	assert.Contains(t, out, "set permissions")
	assert.Contains(t, out, "enabled checks")
}

func TestErrorBox(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.ErrorBox("Authentication Failed", "Token is expired or invalid")
	out := buf.String()
	assert.Contains(t, out, "Authentication Failed")
	assert.Contains(t, out, "Token is expired or invalid")
}

func TestBlank(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.Blank()
	out := buf.String()
	assert.Equal(t, "\n", out)
}

func TestPRLink(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf)
	p.PRLink("fullsend-ai/config", "https://github.com/fullsend-ai/config/pull/42")
	out := buf.String()
	assert.Contains(t, out, "fullsend-ai/config")
	assert.Contains(t, out, "https://github.com/fullsend-ai/config/pull/42")
}
