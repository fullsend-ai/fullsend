package ui

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrinter_Banner(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Banner()

	output := buf.String()
	assert.Contains(t, output, "fullsend")
	assert.Contains(t, output, "autonomous agentic development")
}

func TestPrinter_Header(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Header("Test Header")

	assert.Contains(t, buf.String(), "Test Header")
}

func TestPrinter_StepDone(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.StepDone("Completed task")

	output := buf.String()
	assert.Contains(t, output, "Completed task")
	assert.Contains(t, output, "✓")
}

func TestPrinter_StepFail(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.StepFail("Failed task")

	output := buf.String()
	assert.Contains(t, output, "Failed task")
	assert.Contains(t, output, "✗")
}

func TestPrinter_StepWarn(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.StepWarn("Warning message")

	output := buf.String()
	assert.Contains(t, output, "Warning message")
	assert.Contains(t, output, "!")
}

func TestPrinter_StepInfo(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.StepInfo("Info detail")

	assert.Contains(t, buf.String(), "Info detail")
}

func TestPrinter_KeyValue(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.KeyValue("name", "fullsend")

	output := buf.String()
	assert.Contains(t, output, "name")
	assert.Contains(t, output, "fullsend")
}

func TestPrinter_Table(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Table([][]string{
		{"name", "fullsend"},
		{"version", "1.0.0"},
	})

	output := buf.String()
	assert.Contains(t, output, "name")
	assert.Contains(t, output, "fullsend")
	assert.Contains(t, output, "version")
	assert.Contains(t, output, "1.0.0")
}

func TestPrinter_Table_Empty(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Table(nil)

	assert.Empty(t, buf.String())
}

func TestPrinter_PRLink(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.PRLink("my-repo", "https://github.com/org/repo/pull/1")

	output := buf.String()
	assert.Contains(t, output, "my-repo")
	assert.Contains(t, output, "https://github.com/org/repo/pull/1")
}

func TestPrinter_Summary(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Summary("Done", []string{"Item 1", "Item 2"})

	output := buf.String()
	assert.Contains(t, output, "Done")
	assert.Contains(t, output, "Item 1")
	assert.Contains(t, output, "Item 2")
}

func TestPrinter_ErrorBox(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.ErrorBox("Error Title", "Something went wrong")

	output := buf.String()
	assert.Contains(t, output, "Error Title")
	assert.Contains(t, output, "Something went wrong")
}

func TestPrinter_Blank(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf)

	p.Blank()

	assert.Equal(t, "\n", buf.String())
}

func TestDefaultPrinter(t *testing.T) {
	p := DefaultPrinter()
	assert.NotNil(t, p)
}
