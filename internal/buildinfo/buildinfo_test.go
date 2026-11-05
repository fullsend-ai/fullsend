package buildinfo

import "testing"

func TestDefaults(t *testing.T) {
	if got := Version(); got != "dev" {
		t.Errorf("Version() = %q, want %q", got, "dev")
	}
	if got := CommitSHA(); got != "dev" {
		t.Errorf("CommitSHA() = %q, want %q", got, "dev")
	}
}

func TestAccessorsReturnStampedValues(t *testing.T) {
	origVer, origSHA := version, commitSHA
	t.Cleanup(func() { version, commitSHA = origVer, origSHA })

	// -ldflags -X overwrites these vars at link time; assigning them here
	// simulates a stamped release build.
	version, commitSHA = "v1.2.3", "abc123"
	if got := Version(); got != "v1.2.3" {
		t.Errorf("Version() = %q, want %q", got, "v1.2.3")
	}
	if got := CommitSHA(); got != "abc123" {
		t.Errorf("CommitSHA() = %q, want %q", got, "abc123")
	}
}
