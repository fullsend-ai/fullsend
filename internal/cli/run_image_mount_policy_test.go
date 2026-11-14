package cli

import (
	"testing"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckImageMountPolicyAccess(t *testing.T) {
	tests := []struct {
		name        string
		policy      string
		mounts      []harness.ImageMount
		wantErrPart string
	}{
		{
			name:   "exact read-only path",
			policy: "filesystem_policy:\n  read_only: [/opt/fullsend]\n",
			mounts: []harness.ImageMount{{Dest: "/opt/fullsend"}},
		},
		{
			name:   "read-only ancestor",
			policy: "filesystem_policy:\n  read_only: [/opt]\n",
			mounts: []harness.ImageMount{{Dest: "/opt/fullsend"}},
		},
		{
			name:   "read-write ancestor permits access to read-only mount",
			policy: "filesystem_policy:\n  read_write: [/sandbox]\n",
			mounts: []harness.ImageMount{{Dest: "/sandbox/tools"}},
		},
		{
			name:        "missing access",
			policy:      "filesystem_policy:\n  read_only: [/usr]\n",
			mounts:      []harness.ImageMount{{Dest: "/opt/fullsend"}},
			wantErrPart: `policy "custom-policy" does not grant filesystem access to image mount destination "/opt/fullsend"`,
		},
		{
			name:        "sibling prefix does not grant access",
			policy:      "filesystem_policy:\n  read_only: [/opt/fullsend-tools]\n",
			mounts:      []harness.ImageMount{{Dest: "/opt/fullsend"}},
			wantErrPart: "/opt/fullsend",
		},
		{
			name:        "descendant permission does not grant mount root",
			policy:      "filesystem_policy:\n  read_only: [/opt/fullsend/bin]\n",
			mounts:      []harness.ImageMount{{Dest: "/opt/fullsend"}},
			wantErrPart: "/opt/fullsend",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkImageMountPolicyAccess([]byte(tc.policy), "custom-policy", tc.mounts)
			if tc.wantErrPart == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrPart)
			}
		})
	}
}

func TestCheckImageMountPolicyAccessNoMountsDoesNotParsePolicy(t *testing.T) {
	require.NoError(t, checkImageMountPolicyAccess([]byte("not: [valid"), "legacy", nil))
}
