package repos

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// legacyRotationStateBlob is the exact unversioned document earlier writers
// emitted for a state that uses every legacy field.
const legacyRotationStateBlob = `{"roles":{"analyst":{"phase":"idle"},"poller":{"phase":"overlapping","holder":"h1","lock_until":"2026-09-21T12:00:00Z","incoming_id":7,"outgoing_ids":[3,4],"distributed_at":"2026-09-21T11:00:00Z","expires_at":"2026-10-01","error":"boom"}}}`

// versionedLegacyRotationStateBlob is legacyRotationStateBlob as the current
// writer emits it: the same roles behind the version marker.
const versionedLegacyRotationStateBlob = `{"version":2,` + `"roles":{"analyst":{"phase":"idle"},"poller":{"phase":"overlapping","holder":"h1","lock_until":"2026-09-21T12:00:00Z","incoming_id":7,"outgoing_ids":[3,4],"distributed_at":"2026-09-21T11:00:00Z","expires_at":"2026-10-01","error":"boom"}}}`

func legacyRotationState() rotationStateFile {
	return rotationStateFile{Roles: map[string]rotationRoleState{
		"analyst": {Phase: rotationPhaseIdle},
		"poller": {
			Phase:         rotationPhaseOverlapping,
			Holder:        "h1",
			LockUntil:     "2026-09-21T12:00:00Z",
			IncomingID:    7,
			OutgoingIDs:   []int{3, 4},
			DistributedAt: "2026-09-21T11:00:00Z",
			ExpiresAt:     "2026-10-01",
			Error:         "boom",
		},
	}}
}

func loadRotationStateFrom(t *testing.T, raw string) (rotationStateFile, error) {
	t.Helper()
	fc := provisionClient(t)
	require.NoError(t, fc.UpdateCIVariable(context.Background(), "group", "project", forge.VarGitLabRoleRotation, raw, true))
	file, _, err := loadRotationState(context.Background(), fc, "group", "project")
	return file, err
}

func TestLoadRotationState_Formats(t *testing.T) {
	t.Parallel()
	fullV1 := rotationRoleState{
		Phase:                    rotationPhaseIdle,
		IncomingID:               9,
		DistributedAt:            "2026-09-21T11:00:00Z",
		CreatedTokenIDs:          []int{9, 11},
		ManagedUserID:            101,
		SuppliedUserID:           202,
		SuppliedTokenID:          303,
		Supplied:                 true,
		SuppliedDistributed:      true,
		ExcludedUserIDs:          []int{404, 505},
		GenerationCurrentUserID:  101,
		GenerationPendingUserID:  606,
		GenerationPendingPhase:   "elevated",
		GenerationRetiringUserID: 707,
	}
	tests := []struct {
		name    string
		raw     string
		want    rotationStateFile
		wantErr string
	}{
		{
			name: "legacy unversioned blob",
			raw:  legacyRotationStateBlob,
			want: legacyRotationState(),
		},
		{
			name: "explicit version 0",
			raw:  `{"version":0,"roles":{"analyst":{"phase":"idle"}}}`,
			want: rotationStateFile{Roles: map[string]rotationRoleState{"analyst": {Phase: rotationPhaseIdle}}},
		},
		{
			name: "version 1 blob with all new fields",
			raw: `{"version":1,"roles":{"poller":{"phase":"idle","incoming_id":9,` +
				`"distributed_at":"2026-09-21T11:00:00Z","created_token_ids":[9,11],` +
				`"managed_user_id":101,"supplied_user_id":202,"supplied_token_id":303,` +
				`"supplied":true,"supplied_distributed":true,"excluded_user_ids":[404,505],` +
				`"generation_current_user_id":101,"generation_pending_user_id":606,` +
				`"generation_pending_phase":"elevated","generation_retiring_user_id":707}}}`,
			want: rotationStateFile{Roles: map[string]rotationRoleState{"poller": fullV1}},
		},
		{
			name: "version 2 blob with all new fields",
			raw: `{"version":2,"roles":{"poller":{"phase":"idle","incoming_id":9,` +
				`"distributed_at":"2026-09-21T11:00:00Z","created_token_ids":[9,11],` +
				`"managed_user_id":101,"supplied_user_id":202,"supplied_token_id":303,` +
				`"supplied":true,"supplied_distributed":true,"excluded_user_ids":[404,505],` +
				`"generation_current_user_id":101,"generation_pending_user_id":606,` +
				`"generation_pending_phase":"elevated","generation_retiring_user_id":707}}}`,
			want: rotationStateFile{Roles: map[string]rotationRoleState{"poller": fullV1}},
		},
		{
			name: "unknown keys at envelope and role level",
			raw:  `{"version":1,"future":{"x":1},"roles":{"poller":{"phase":"idle","future_role_key":[1,2]}}}`,
			want: rotationStateFile{Roles: map[string]rotationRoleState{"poller": {Phase: rotationPhaseIdle}}},
		},
		{
			name: "version without roles",
			raw:  `{"version":1}`,
			want: rotationStateFile{Roles: map[string]rotationRoleState{}},
		},
		{
			name:    "invalid lock_until timestamp",
			raw:     `{"version":1,"roles":{"poller":{"lock_until":"not-a-time"}}}`,
			wantErr: "invalid GitLab role rotation state poller.lock_until",
		},
		{
			name:    "invalid distributed_at timestamp",
			raw:     `{"roles":{"poller":{"distributed_at":"yesterday"}}}`,
			wantErr: "invalid GitLab role rotation state poller.distributed_at",
		},
		{
			name:    "unsupported future version",
			raw:     `{"version":3,"roles":{}}`,
			wantErr: "unsupported GitLab role rotation state version 3",
		},
		{
			name:    "negative version",
			raw:     `{"version":-1,"roles":{}}`,
			wantErr: "unsupported GitLab role rotation state version -1",
		},
		{
			name:    "malformed JSON",
			raw:     `{"roles":`,
			wantErr: "decode GitLab role rotation state",
		},
		{
			name:    "wrong type for known field",
			raw:     `{"roles":{"poller":{"managed_user_id":"abc"}}}`,
			wantErr: "decode GitLab role rotation state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := loadRotationStateFrom(t, tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestWriteRotationState_VersionedBytes is the golden check that the writer
// emits the version 2 format byte-for-byte, with the marker first.
func TestWriteRotationState_VersionedBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state rotationStateFile
		want  string
	}{
		{name: "nil roles", state: rotationStateFile{}, want: `{"version":2,"roles":{}}`},
		{name: "legacy fields", state: legacyRotationState(), want: versionedLegacyRotationStateBlob},
		{
			name: "ownership fields",
			state: rotationStateFile{Roles: map[string]rotationRoleState{"coder": {
				Phase: rotationPhaseIdle, CreatedTokenIDs: []int{5}, ManagedUserID: 42,
				SuppliedUserID: 7, SuppliedTokenID: 8, Supplied: true, ExcludedUserIDs: []int{7},
			}}},
			want: `{"version":2,"roles":{"coder":{"phase":"idle","created_token_ids":[5],"managed_user_id":42,` +
				`"supplied_user_id":7,"supplied_token_id":8,"supplied":true,"excluded_user_ids":[7]}}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fc := provisionClient(t)
			require.NoError(t, writeRotationState(context.Background(), fc, "group", "project", tt.state))
			assert.Equal(t, tt.want, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation])
		})
	}
}

func TestRotationState_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("legacy blob rewrites with the version marker", func(t *testing.T) {
		t.Parallel()
		fc := provisionClient(t)
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, legacyRotationStateBlob, true))
		file, _, err := loadRotationState(ctx, fc, "group", "project")
		require.NoError(t, err)
		require.NoError(t, writeRotationState(ctx, fc, "group", "project", file))
		assert.Equal(t, versionedLegacyRotationStateBlob, fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation])

		reloaded, _, err := loadRotationState(ctx, fc, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, file, reloaded)
	})

	t.Run("version 1 blob keeps known fields and is rewritten as version 2", func(t *testing.T) {
		t.Parallel()
		fc := provisionClient(t)
		raw := `{"version":1,"extra":true,"roles":{"poller":{"phase":"idle","created_token_ids":[5],"managed_user_id":42,"generation_pending_phase":"verified","unknown":1}}}`
		require.NoError(t, fc.UpdateCIVariable(ctx, "group", "project", forge.VarGitLabRoleRotation, raw, true))
		file, _, err := loadRotationState(ctx, fc, "group", "project")
		require.NoError(t, err)
		require.NoError(t, writeRotationState(ctx, fc, "group", "project", file))
		assert.Equal(t,
			`{"version":2,"roles":{"poller":{"phase":"idle","created_token_ids":[5],"managed_user_id":42,"generation_pending_phase":"verified"}}}`,
			fc.VariableValues["group/project/"+forge.VarGitLabRoleRotation])

		reloaded, _, err := loadRotationState(ctx, fc, "group", "project")
		require.NoError(t, err)
		assert.Equal(t, file, reloaded)
	})
}
