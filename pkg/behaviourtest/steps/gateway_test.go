package steps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTestGatewayFromEnv(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "")
	_, _, ok := testGatewayFromEnv()
	assert.False(t, ok)

	t.Setenv(envInferenceGatewayURL, " https://gw.example/ ")
	t.Setenv(envInferenceGatewayAudience, "")
	u, aud, ok := testGatewayFromEnv()
	require.True(t, ok)
	assert.Equal(t, "https://gw.example", u)
	assert.Equal(t, defaultGatewayAudience, aud)

	t.Setenv(envInferenceGatewayAudience, "custom-aud")
	_, aud, _ = testGatewayFromEnv()
	assert.Equal(t, "custom-aud", aud)
}

func TestExpandGatewayPlaceholder(t *testing.T) {
	t.Setenv(envInferenceGatewayURL, "")
	got, err := expandGatewayPlaceholder("GET https://other.example/ TOK")
	require.NoError(t, err)
	assert.Equal(t, "GET https://other.example/ TOK", got)
	_, err = expandGatewayPlaceholder("GET <gateway>/v1/models TOK")
	assert.Error(t, err)

	t.Setenv(envInferenceGatewayURL, "https://gw.example/")
	got, err = expandGatewayPlaceholder("GET <gateway>/v1/models TOK")
	require.NoError(t, err)
	assert.Equal(t, "GET https://gw.example/v1/models TOK", got)
}

func TestWithInferenceGatewayKeepsOtherKeys(t *testing.T) {
	t.Parallel()

	out, err := withInferenceGateway([]byte("runtime: dummy\ninference:\n  openai:\n    project: p\n"), "https://gw.example", "aud")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(out, &doc))
	assert.Equal(t, "dummy", doc["runtime"])
	inference := doc["inference"].(map[string]any)
	assert.Equal(t, map[string]any{"project": "p"}, inference["openai"])
	assert.Equal(t, map[string]any{"url": "https://gw.example", "audience": "aud"}, inference["gateway"])

	out, err = withInferenceGateway(nil, "https://gw.example", "aud")
	require.NoError(t, err)
	assert.Contains(t, string(out), "audience: aud")

	_, err = withInferenceGateway([]byte("{"), "u", "a")
	assert.Error(t, err)
}

func TestProbeResultAssertions(t *testing.T) {
	t.Parallel()

	data := []byte(`{"operations":[{"description":"chat","success":true,"http_status":200,"response_body":"{\"x-api-key\":\"stub\"}"},{"description":"denied","success":false,"http_status":403}]}`)
	res, err := probeResultFrom(data, " chat ")
	require.NoError(t, err)
	require.NoError(t, checkProbeStatus(res, "200"))
	assert.Error(t, checkProbeStatus(res, "403"))
	require.NoError(t, checkProbeBody(res, true, "x-api-key"))
	require.NoError(t, checkProbeBody(res, false, "eyJ"))
	assert.Error(t, checkProbeBody(res, false, "stub"))
	assert.Error(t, checkProbeBody(res, true, "eyJ"))
	assert.Error(t, checkProbeBody(res, true, ""))

	res, err = probeResultFrom(data, "denied")
	require.NoError(t, err)
	require.NoError(t, checkProbeStatus(res, "403"))

	_, err = probeResultFrom(data, "missing")
	assert.Error(t, err)
	_, err = probeResultFrom([]byte("{"), "chat")
	assert.Error(t, err)
}

// TestProbeBodyRedactedJWT checks the custody assertion against a body
// whose JWT the runtime redacted: the flag, not the redacted text, decides.
func TestProbeBodyRedactedJWT(t *testing.T) {
	t.Parallel()

	data := []byte(`{"operations":[{"description":"leak","success":true,"http_status":200,"response_body":"{\"authorization\":\"Bearer <redacted-jwt>\"}","body_had_jwt":true}]}`)
	res, err := probeResultFrom(data, "leak")
	require.NoError(t, err)
	require.True(t, res.BodyHadJWT)
	err = checkProbeBody(res, false, "eyJ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpectedly contains")
	require.NoError(t, checkProbeBody(res, true, "eyJ"))
	// Other needles still match the recorded text only.
	require.NoError(t, checkProbeBody(res, false, "x-api-key"))
	require.NoError(t, checkProbeBody(res, true, "<redacted-jwt>"))
}
