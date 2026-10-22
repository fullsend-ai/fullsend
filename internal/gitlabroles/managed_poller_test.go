package gitlabroles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
			assert.Equal(t, tc.want, IsManagedPollerTokenName(tc.name), "IsManagedPollerTokenName(%q)", tc.name)
		})
	}
}
