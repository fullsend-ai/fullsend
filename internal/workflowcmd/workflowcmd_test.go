package workflowcmd

import (
	"strings"
	"testing"
)

func TestSanitizeAnnotation_NoDoubleColon(t *testing.T) {
	for _, in := range []string{"::", ":::", "::::", ":::::", "a:::error:::b", ":%0A::", "::\n::"} {
		got := SanitizeAnnotation(in)
		if strings.Contains(got, "::") {
			t.Errorf("SanitizeAnnotation(%q) = %q, still contains %q", in, got, "::")
		}
	}
}

func TestSanitizeAnnotation_EncodesAndStripsControl(t *testing.T) {
	got := SanitizeAnnotation("a%b\r\nc\x1b[31m")
	if strings.ContainsAny(got, "\r\n\x1b") {
		t.Errorf("SanitizeAnnotation kept a raw control character: %q", got)
	}
	if got != "a%25b%0D%0Ac[31m" {
		t.Errorf("SanitizeAnnotation = %q", got)
	}
}
