package appsetup

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUploadAppLogo(t *testing.T) {
	fakeLogo := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}

	var gotAuth string
	var gotBody []byte
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/app", r.URL.Path)

		gotAuth = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		gotBody = body

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "fake-jwt-token", fakeLogo)
	require.NoError(t, err)

	assert.Equal(t, "Bearer fake-jwt-token", gotAuth)

	mediaType, params, err := mime.ParseMediaType(gotContentType)
	require.NoError(t, err)
	assert.Equal(t, "multipart/form-data", mediaType)

	reader := multipart.NewReader(bytes.NewReader(gotBody), params["boundary"])
	part, err := reader.NextPart()
	require.NoError(t, err)
	assert.Equal(t, "logo", part.FormName())
	assert.Equal(t, "logo.png", part.FileName())

	partBody, err := io.ReadAll(part)
	require.NoError(t, err)
	assert.Equal(t, fakeLogo, partBody)
}

func TestUploadAppLogo_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := uploadAppLogo(context.Background(), server.URL, "token", []byte("png"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "500")
}
