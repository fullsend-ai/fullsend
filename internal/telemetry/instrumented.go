package telemetry

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

// InstrumentedPrinter unifies terminal output (ui.Printer) and structured
// telemetry recording (Recorder) into a single call site. Every printed
// lifecycle step is automatically recorded as a telemetry event and OTEL
// span. This makes it structurally impossible to print a step without
// tracing it.
//
// The recorder may be attached after construction (via AttachRecorder)
// to handle the bootstrapping period where the printer is needed before
// the run directory exists. Steps started before the recorder is attached
// are captured as buffered entries and replayed once the recorder becomes
// available.
type InstrumentedPrinter struct {
	printer  *ui.Printer
	rec      *Recorder
	ctx      context.Context
	buffered []bufferedStep
}

type bufferedStep struct {
	name  string
	start time.Time
	done  bool
	fail  error
	warn  string
	attrs []Attr
}

// NewInstrumentedPrinter creates an InstrumentedPrinter that writes styled
// output to w. The recorder is not yet attached — call AttachRecorder once
// the run directory and tracer are available.
func NewInstrumentedPrinter(w io.Writer) *InstrumentedPrinter {
	return &InstrumentedPrinter{
		printer: ui.New(w),
		ctx:     context.Background(),
	}
}

// AttachRecorder connects the telemetry recorder and replays any buffered
// steps that occurred before the recorder was available.
func (ip *InstrumentedPrinter) AttachRecorder(rec *Recorder, ctx context.Context) {
	ip.rec = rec
	ip.ctx = ctx
	ip.replayBuffered()
}

// Recorder returns the underlying Recorder (may be nil before AttachRecorder).
func (ip *InstrumentedPrinter) Recorder() *Recorder {
	return ip.rec
}

// Context returns the current run context (root span context after attach).
func (ip *InstrumentedPrinter) Context() context.Context {
	return ip.ctx
}

// StepStart prints a step-in-progress marker and records a telemetry event.
func (ip *InstrumentedPrinter) StepStart(name, msg string, attrs ...Attr) {
	ip.printer.StepStart(msg)
	if ip.rec != nil {
		ip.rec.StepStart(ip.ctx, name, attrs...)
	} else {
		ip.buffered = append(ip.buffered, bufferedStep{name: name, start: time.Now(), attrs: attrs})
	}
}

// StepDone prints a success marker and records step completion.
func (ip *InstrumentedPrinter) StepDone(name, msg string, attrs ...Attr) {
	ip.printer.StepDone(msg)
	if ip.rec != nil {
		ip.rec.StepDone(name, attrs...)
	} else {
		ip.markBuffered(name, func(b *bufferedStep) { b.done = true; b.attrs = append(b.attrs, attrs...) })
	}
}

// StepFail prints a failure marker and records the step as failed.
func (ip *InstrumentedPrinter) StepFail(name, msg string, err error) {
	ip.printer.StepFail(msg)
	if ip.rec != nil {
		ip.rec.StepFail(name, err)
	} else {
		ip.markBuffered(name, func(b *bufferedStep) { b.fail = err })
	}
}

// StepWarn prints a warning marker and records the step with a warning.
func (ip *InstrumentedPrinter) StepWarn(name, msg string) {
	ip.printer.StepWarn(msg)
	if ip.rec != nil {
		ip.rec.StepWarn(name, msg)
	} else {
		ip.markBuffered(name, func(b *bufferedStep) { b.warn = msg })
	}
}

// StepInfo prints indented informational text (no telemetry event).
func (ip *InstrumentedPrinter) StepInfo(text string) {
	ip.printer.StepInfo(text)
}

// AddEvent attaches a log-style event to the currently-open step span.
// This is the standard OTEL mechanism for adding context to spans and
// appears in all backends (Jaeger, Phoenix, Tempo) under "Events" or "Logs".
func (ip *InstrumentedPrinter) AddEvent(stepName, eventName string, attrs ...Attr) {
	if ip.rec != nil {
		ip.rec.AddEvent(stepName, eventName, attrs...)
	}
}

// AddRootEvent attaches an event directly to the root span.
func (ip *InstrumentedPrinter) AddRootEvent(eventName string, attrs ...Attr) {
	if ip.rec != nil {
		ip.rec.AddRootEvent(eventName, attrs...)
	}
}

// Warn prints a standalone warning not associated with a step lifecycle.
// Use for informational warnings that aren't closing a span.
func (ip *InstrumentedPrinter) Warn(msg string) {
	ip.printer.StepWarn(msg)
}

// Printer returns the underlying ui.Printer for helpers that only need
// informational output (heartbeats, progress parsing) without step lifecycles.
func (ip *InstrumentedPrinter) Printer() *ui.Printer {
	return ip.printer
}

// Banner prints the fullsend brand banner.
func (ip *InstrumentedPrinter) Banner() {
	ip.printer.Banner()
}

// Header prints a section header.
func (ip *InstrumentedPrinter) Header(text string) {
	ip.printer.Header(text)
}

// KeyValue prints a key-value pair.
func (ip *InstrumentedPrinter) KeyValue(key, value string) {
	ip.printer.KeyValue(key, value)
}

// Summary prints a bordered summary box.
func (ip *InstrumentedPrinter) Summary(title string, items []string) {
	ip.printer.Summary(title, items)
}

// ErrorBox prints an error-styled bordered box.
func (ip *InstrumentedPrinter) ErrorBox(title, detail string) {
	ip.printer.ErrorBox(title, detail)
}

// Heartbeat prints a periodic progress line.
func (ip *InstrumentedPrinter) Heartbeat(text string) {
	ip.printer.Heartbeat(text)
}

// Blank prints an empty line.
func (ip *InstrumentedPrinter) Blank() {
	ip.printer.Blank()
}

// Raw writes text directly without styling.
func (ip *InstrumentedPrinter) Raw(text string) {
	ip.printer.Raw(text)
}

// PRLink prints a pull request link.
func (ip *InstrumentedPrinter) PRLink(repo, url string) {
	ip.printer.PRLink(repo, url)
}

// TimedMsg formats a message with elapsed seconds appended.
func TimedMsg(base string, d time.Duration) string {
	return fmt.Sprintf("%s (%.1fs)", base, d.Seconds())
}

func (ip *InstrumentedPrinter) replayBuffered() {
	for _, b := range ip.buffered {
		ip.rec.StepStart(ip.ctx, b.name, b.attrs...)
		switch {
		case b.fail != nil:
			ip.rec.StepFail(b.name, b.fail)
		case b.warn != "":
			ip.rec.StepWarn(b.name, b.warn)
		case b.done:
			ip.rec.StepDone(b.name, b.attrs...)
		}
	}
	ip.buffered = nil
}

func (ip *InstrumentedPrinter) markBuffered(name string, fn func(*bufferedStep)) {
	for i := len(ip.buffered) - 1; i >= 0; i-- {
		if ip.buffered[i].name == name {
			fn(&ip.buffered[i])
			return
		}
	}
}
