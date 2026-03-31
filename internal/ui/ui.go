// Package ui provides styled terminal output for the fullsend CLI.
//
// It uses charmbracelet/lipgloss for styling and provides consistent
// formatting for status messages, progress indicators, and results.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Styles used throughout the CLI output.
var (
	// Brand colors
	brandColor   = lipgloss.Color("#7C3AED") // purple-600
	successColor = lipgloss.Color("#10B981") // emerald-500
	warningColor = lipgloss.Color("#F59E0B") // amber-500
	errorColor   = lipgloss.Color("#EF4444") // red-500
	mutedColor   = lipgloss.Color("#6B7280") // gray-500
	infoColor    = lipgloss.Color("#3B82F6") // blue-500

	// Text styles
	bold         = lipgloss.NewStyle().Bold(true)
	muted        = lipgloss.NewStyle().Foreground(mutedColor)
	successStyle = lipgloss.NewStyle().Foreground(successColor)
	warningStyle = lipgloss.NewStyle().Foreground(warningColor)
	errorStyle   = lipgloss.NewStyle().Foreground(errorColor)
	infoStyle    = lipgloss.NewStyle().Foreground(infoColor)
	brand        = lipgloss.NewStyle().Foreground(brandColor).Bold(true)

	// Step indicators
	checkMark = successStyle.Render("✓")
	crossMark = errorStyle.Render("✗")
	arrow     = brand.Render("→")
	bullet    = muted.Render("•")
	warnMark  = warningStyle.Render("!")
)

// Printer handles styled output to a writer. All CLI output goes through
// this so we can control destination and style consistently.
type Printer struct {
	w io.Writer
}

// NewPrinter creates a Printer that writes to the given writer.
func NewPrinter(w io.Writer) *Printer {
	return &Printer{w: w}
}

// DefaultPrinter writes to stdout.
func DefaultPrinter() *Printer {
	return NewPrinter(os.Stdout)
}

// Banner prints the fullsend banner at the start of a session.
func (p *Printer) Banner() {
	banner := lipgloss.NewStyle().
		Bold(true).
		Foreground(brandColor).
		Render("⚡ fullsend")

	tagline := muted.Render("autonomous agentic development for GitHub")

	_, _ = fmt.Fprintf(p.w, "\n  %s  %s\n\n", banner, tagline)
}

// Header prints a section header.
func (p *Printer) Header(text string) {
	_, _ = fmt.Fprintf(p.w, "\n  %s %s\n", arrow, bold.Render(text))
}

// StepStart prints the beginning of a step (before completion is known).
func (p *Printer) StepStart(text string) {
	_, _ = fmt.Fprintf(p.w, "  %s %s\n", bullet, text)
}

// StepDone prints a completed step with a check mark.
func (p *Printer) StepDone(text string) {
	_, _ = fmt.Fprintf(p.w, "  %s %s\n", checkMark, text)
}

// StepFail prints a failed step with a cross mark.
func (p *Printer) StepFail(text string) {
	_, _ = fmt.Fprintf(p.w, "  %s %s\n", crossMark, text)
}

// StepWarn prints a warning step.
func (p *Printer) StepWarn(text string) {
	_, _ = fmt.Fprintf(p.w, "  %s %s\n", warnMark, text)
}

// StepInfo prints an informational detail, indented under a step.
func (p *Printer) StepInfo(text string) {
	_, _ = fmt.Fprintf(p.w, "      %s\n", muted.Render(text))
}

// KeyValue prints a key-value pair with styled formatting.
func (p *Printer) KeyValue(key, value string) {
	k := muted.Render(key + ":")
	_, _ = fmt.Fprintf(p.w, "    %s %s\n", k, value)
}

// Table prints a simple table of key-value pairs with aligned columns.
func (p *Printer) Table(rows [][]string) {
	if len(rows) == 0 {
		return
	}

	// Find max key length
	maxKey := 0
	for _, row := range rows {
		if len(row) >= 2 && len(row[0]) > maxKey {
			maxKey = len(row[0])
		}
	}

	for _, row := range rows {
		if len(row) >= 2 {
			padding := strings.Repeat(" ", maxKey-len(row[0]))
			k := muted.Render(row[0] + ":" + padding)
			_, _ = fmt.Fprintf(p.w, "    %s  %s\n", k, row[1])
		}
	}
}

// PRLink prints a styled PR URL.
func (p *Printer) PRLink(repo, url string) {
	_, _ = fmt.Fprintf(p.w, "    %s %s\n", infoStyle.Render(repo), muted.Render(url))
}

// Summary prints a summary box at the end of a command.
func (p *Printer) Summary(title string, items []string) {
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(brandColor).
		Padding(0, 2)

	header := bold.Render(title)
	lines := make([]string, 0, len(items)+2)
	lines = append(lines, header, "")
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("  %s %s", checkMark, item))
	}

	content := strings.Join(lines, "\n")
	_, _ = fmt.Fprintf(p.w, "\n%s\n\n", boxStyle.Render(content))
}

// ErrorBox prints an error message in a styled box.
func (p *Printer) ErrorBox(title, detail string) {
	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(errorColor).
		Padding(0, 2)

	header := lipgloss.NewStyle().Foreground(errorColor).Bold(true).Render(title)
	content := header + "\n\n  " + detail

	_, _ = fmt.Fprintf(p.w, "\n%s\n\n", boxStyle.Render(content))
}

// Blank prints an empty line.
func (p *Printer) Blank() {
	_, _ = fmt.Fprintln(p.w)
}
