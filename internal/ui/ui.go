package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Color constants for consistent terminal styling.
var (
	ColorBrand   = lipgloss.Color("#7C3AED")
	ColorSuccess = lipgloss.Color("#10B981")
	ColorWarning = lipgloss.Color("#F59E0B")
	ColorError   = lipgloss.Color("#EF4444")
	ColorMuted   = lipgloss.Color("#6B7280")
	ColorInfo    = lipgloss.Color("#3B82F6")
)

// Printer provides styled terminal output.
type Printer struct {
	w io.Writer
}

// New creates a new Printer writing to w.
func New(w io.Writer) *Printer {
	return &Printer{w: w}
}

// Banner prints the fullsend brand banner with tagline.
func (p *Printer) Banner() {
	brand := lipgloss.NewStyle().Bold(true).Foreground(ColorBrand).Render("fullsend")
	fmt.Fprintf(p.w, "\u26a1 %s\n", brand)
	tagline := lipgloss.NewStyle().Foreground(ColorMuted).Render("Autonomous agentic development for GitHub organizations")
	fmt.Fprintf(p.w, "  %s\n", tagline)
}

// Header prints a section header with an arrow prefix.
func (p *Printer) Header(text string) {
	styled := lipgloss.NewStyle().Bold(true).Render(text)
	fmt.Fprintf(p.w, "\u2192 %s\n", styled)
}

// StepStart prints a step-in-progress marker.
func (p *Printer) StepStart(text string) {
	fmt.Fprintf(p.w, "  \u2022 %s\n", text)
}

// StepDone prints a successful step marker in success color.
func (p *Printer) StepDone(text string) {
	styled := lipgloss.NewStyle().Foreground(ColorSuccess).Render("\u2713 " + text)
	fmt.Fprintf(p.w, "  %s\n", styled)
}

// StepFail prints a failed step marker in error color.
func (p *Printer) StepFail(text string) {
	styled := lipgloss.NewStyle().Foreground(ColorError).Render("\u2717 " + text)
	fmt.Fprintf(p.w, "  %s\n", styled)
}

// StepWarn prints a warning step marker in warning color.
func (p *Printer) StepWarn(text string) {
	styled := lipgloss.NewStyle().Foreground(ColorWarning).Render("! " + text)
	fmt.Fprintf(p.w, "  %s\n", styled)
}

// StepInfo prints indented informational text in muted color.
func (p *Printer) StepInfo(text string) {
	styled := lipgloss.NewStyle().Foreground(ColorMuted).Render(text)
	fmt.Fprintf(p.w, "    %s\n", styled)
}

// KeyValue prints a key-value pair with the key in muted color.
func (p *Printer) KeyValue(key, value string) {
	k := lipgloss.NewStyle().Foreground(ColorMuted).Render(key + ":")
	fmt.Fprintf(p.w, "    %s %s\n", k, value)
}

// Summary prints a bordered summary box with a title and list of items.
func (p *Printer) Summary(title string, items []string) {
	var content strings.Builder
	content.WriteString(lipgloss.NewStyle().Bold(true).Render(title))
	content.WriteString("\n")
	for _, item := range items {
		content.WriteString("  " + item + "\n")
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBrand).
		Padding(0, 1).
		Render(content.String())

	fmt.Fprintln(p.w, box)
}

// ErrorBox prints an error-styled bordered box with title and detail.
func (p *Printer) ErrorBox(title, detail string) {
	heading := lipgloss.NewStyle().Bold(true).Foreground(ColorError).Render(title)
	body := heading + "\n" + detail

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorError).
		Padding(0, 1).
		Render(body)

	fmt.Fprintln(p.w, box)
}

// Blank prints an empty line.
func (p *Printer) Blank() {
	fmt.Fprintln(p.w)
}

// PRLink prints a pull request link with the repository name.
func (p *Printer) PRLink(repo, url string) {
	repoStyled := lipgloss.NewStyle().Bold(true).Render(repo)
	urlStyled := lipgloss.NewStyle().Foreground(ColorInfo).Render(url)
	fmt.Fprintf(p.w, "  %s %s\n", repoStyled, urlStyled)
}
