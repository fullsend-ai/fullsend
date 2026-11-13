package workflowcmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/security"
)

func suppress(t *testing.T) {
	t.Helper()
	SetSuppressed(true)
	t.Cleanup(func() { SetSuppressed(false) })
}

func TestFprintf(t *testing.T) {
	var out bytes.Buffer
	Fprintf(&out, "::notice::%s\n", "synthetic")
	assert.Equal(t, "::notice::synthetic\n", out.String())

	suppress(t)
	out.Reset()
	Fprintf(&out, "::notice::%s\n", "synthetic")
	assert.Empty(t, out.String())
}

func TestIsCommandLine(t *testing.T) {
	for line, want := range map[string]bool{
		"::notice::hello":               true,
		"::error title=T::hello\r":      true,
		"  ::warning::indented":         true, // the runner trims leading spaces
		"::add-mask::value":             true,
		"::stop-commands::abc123":       true,
		"::abc123::":                    true, // resume token line
		"::group::title":                true,
		"::endgroup::":                  true,
		"plain":                         false,
		"text ::notice::mid-line":       false,
		"::":                            false,
		"::notice":                      false,
		"##[error]logging command form": false,
	} {
		assert.Equal(t, want, IsCommandLine(line), "%q", line)
	}
}

func TestLineFilter_PassesThroughWhenOff(t *testing.T) {
	var out bytes.Buffer
	f := FilterLines(&out)
	_, err := f.Write([]byte("::notice::kept\nplain\n::partial"))
	require.NoError(t, err)
	require.NoError(t, f.Flush())
	assert.Equal(t, "::notice::kept\nplain\n::partial", out.String())
}

func TestLineFilter_DropsCommandsWhenSuppressed(t *testing.T) {
	suppress(t)
	const secret = "synthetic-filter-secret\nsecond-line"
	var out bytes.Buffer
	f := FilterLines(&out)
	// Writes split mid-line, including inside a command, and a final line
	// without a newline.
	for _, chunk := range []string{
		"before\n::no", "tice::dropped\n",
		"::stop-commands::tok123\n",
		"::error::inside the block\n",
		"::tok123::\n",
		"::add-mask::synthetic-filter-secret%0Asecond-line\n",
		"after\n::group::last",
	} {
		_, err := f.Write([]byte(chunk))
		require.NoError(t, err)
	}
	require.NoError(t, f.Flush())

	assert.Equal(t, "before\nafter\n", out.String())
	scan := security.NewSecretRedactor().Scan("log " + secret + " here")
	assert.False(t, scan.Safe, "a dropped add-mask value is registered for redaction")
}

// The runner matches command names case-insensitively, so an upper- or
// mixed-case add-mask is dropped and its (percent-encoded) value registered.
func TestLineFilter_AddMaskNameIsCaseInsensitive(t *testing.T) {
	suppress(t)
	var out bytes.Buffer
	f := FilterLines(&out)
	for _, line := range []string{
		"::ADD-MASK::synthetic-upper-case-secret\n",
		"::Add-Mask::synthetic-mixed%0Acase-secret\n",
		"::add-MASK::synthetic-100%25-case-secret\n",
	} {
		_, err := f.Write([]byte(line))
		require.NoError(t, err)
	}
	assert.Empty(t, out.String())
	for _, secret := range []string{
		"synthetic-upper-case-secret",
		"synthetic-mixed\ncase-secret",
		"synthetic-100%-case-secret",
	} {
		scan := security.NewSecretRedactor().Scan("log " + secret + " here")
		assert.False(t, scan.Safe, "%q must be registered for redaction", secret)
	}
}

// A masked value the redactor refuses (too short) is not covered anywhere,
// so the filter writes a warning that carries none of it.
func TestLineFilter_ShortMaskValueWarns(t *testing.T) {
	suppress(t)
	var out bytes.Buffer
	f := FilterLines(&out)
	_, err := f.Write([]byte("::add-mask::abc\nplain\n"))
	require.NoError(t, err)
	assert.Equal(t, shortMaskWarning+"plain\n", out.String())
	assert.NotContains(t, out.String(), "abc")
}

func TestLineFilter_KeepsPartialPlainLine(t *testing.T) {
	suppress(t)
	var out bytes.Buffer
	f := FilterLines(&out)
	_, err := f.Write([]byte("no newline"))
	require.NoError(t, err)
	assert.Empty(t, out.String())
	require.NoError(t, f.Flush())
	assert.Equal(t, "no newline", out.String())
}
