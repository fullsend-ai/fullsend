package botidentity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMintBotRoleWireContract ties the client types to the golden fixture
// that internal/mintcore also loads (bot_role_wire_test.go there), so the
// duplicated client and server definitions cannot drift apart unnoticed.
func TestMintBotRoleWireContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "bot_role_wire.json"))
	require.NoError(t, err)
	var f struct {
		Path               string          `json:"path"`
		Request            json.RawMessage `json:"request"`
		OutcomeRecognized  string          `json:"outcome_recognized"`
		OutcomeNoMatch     string          `json:"outcome_no_match"`
		ResponseRecognized json.RawMessage `json:"response_recognized"`
		ResponseNoMatch    json.RawMessage `json:"response_no_match"`
	}
	require.NoError(t, json.Unmarshal(data, &f))

	require.Equal(t, f.Path, MintBotRolePath)
	require.Equal(t, f.OutcomeRecognized, MintOutcomeRecognized)
	require.Equal(t, f.OutcomeNoMatch, MintOutcomeNoMatch)

	roundTrip := func(t *testing.T, fixture json.RawMessage, v any) {
		t.Helper()
		dec := json.NewDecoder(bytes.NewReader(fixture))
		dec.DisallowUnknownFields()
		require.NoError(t, dec.Decode(v))
		out, err := json.Marshal(v)
		require.NoError(t, err)
		var got, want any
		require.NoError(t, json.Unmarshal(out, &got))
		require.NoError(t, json.Unmarshal(fixture, &want))
		require.True(t, reflect.DeepEqual(want, got), "encodes as %s, fixture is %s", out, fixture)
	}

	t.Run("request", func(t *testing.T) { roundTrip(t, f.Request, &MintBotRoleRequest{}) })
	t.Run("recognized response", func(t *testing.T) { roundTrip(t, f.ResponseRecognized, &MintBotRoleResponse{}) })
	t.Run("no-match response", func(t *testing.T) { roundTrip(t, f.ResponseNoMatch, &MintBotRoleResponse{}) })
}
