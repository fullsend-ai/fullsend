package mintcore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// botRoleWireFixture is the shared golden fixture of the POST /v1/bot-role
// wire contract. internal/botidentity loads the same file in its tests, so
// the duplicated client and server types cannot drift apart unnoticed.
type botRoleWireFixture struct {
	Path               string          `json:"path"`
	Request            json.RawMessage `json:"request"`
	OutcomeRecognized  string          `json:"outcome_recognized"`
	OutcomeNoMatch     string          `json:"outcome_no_match"`
	ResponseRecognized json.RawMessage `json:"response_recognized"`
	ResponseNoMatch    json.RawMessage `json:"response_no_match"`
}

func loadBotRoleWireFixture(t *testing.T) botRoleWireFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "botidentity", "testdata", "bot_role_wire.json"))
	if err != nil {
		t.Fatalf("reading wire fixture: %v", err)
	}
	var f botRoleWireFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decoding wire fixture: %v", err)
	}
	return f
}

// sameJSON reports whether a and b encode the same JSON value.
func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("decoding %s: %v", a, err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("decoding %s: %v", b, err)
	}
	return reflect.DeepEqual(av, bv)
}

// strictDecode decodes data into v and fails on any field v does not declare.
func strictDecode(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("strict decoding %s: %v", data, err)
	}
}

func TestBotRoleWireContract(t *testing.T) {
	f := loadBotRoleWireFixture(t)

	if botRolePath != f.Path {
		t.Errorf("botRolePath = %q, fixture path = %q", botRolePath, f.Path)
	}
	if botRoleOutcomeRecognized != f.OutcomeRecognized {
		t.Errorf("recognized outcome = %q, fixture = %q", botRoleOutcomeRecognized, f.OutcomeRecognized)
	}
	if botRoleOutcomeNoMatch != f.OutcomeNoMatch {
		t.Errorf("no-match outcome = %q, fixture = %q", botRoleOutcomeNoMatch, f.OutcomeNoMatch)
	}

	t.Run("request", func(t *testing.T) {
		var req botRoleRequest
		strictDecode(t, f.Request, &req)
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		if !sameJSON(t, out, f.Request) {
			t.Errorf("request encodes as %s, fixture is %s", out, f.Request)
		}
	})

	responses := []struct {
		name    string
		fixture json.RawMessage
	}{
		{"recognized response", f.ResponseRecognized},
		{"no-match response", f.ResponseNoMatch},
	}
	for _, tt := range responses {
		t.Run(tt.name, func(t *testing.T) {
			var resp botRoleResponse
			strictDecode(t, tt.fixture, &resp)
			out, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, out, tt.fixture) {
				t.Errorf("response encodes as %s, fixture is %s", out, tt.fixture)
			}
		})
	}
}
