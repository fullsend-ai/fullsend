package gitlabroles

import "testing"

func TestIsManagedPollerTokenName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{PollerTokenName, true}, {PollerBootstrapTokenName, true},
		{AnalystTokenName, false}, {CoderTokenName, false}, {"personal-token", false},
		{"FULLSEND-POLLER", false}, {"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsManagedPollerTokenName(tc.name); got != tc.want {
				t.Errorf("IsManagedPollerTokenName(%q) = %t, want %t", tc.name, got, tc.want)
			}
		})
	}
}
