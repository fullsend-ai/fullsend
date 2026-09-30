package github

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlobJSONLength(t *testing.T) {
	for _, size := range []int{0, 1, 2, 3, 4, 1024, 100 * 1024} {
		assert.Equal(t, int64(len(blobJSONPrefix)+base64.StdEncoding.EncodedLen(size)+len(blobJSONSuffix)), blobJSONLength(int64(size)))
	}
}

func TestBlobJSONReadCloser_RoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0x00},
		[]byte("ab"),
		[]byte("abc"),
		[]byte("abcd"),
		{0x7f, 0x45, 0x4c, 0x46, 0xff, 0xfe, 0x00, 0x01, 0x02},
		bytesOf(1024, 0x5a),
	}
	for _, content := range cases {
		src := io.NopCloser(strings.NewReader(string(content)))
		r := newBlobJSONReadCloser(src)
		body, err := io.ReadAll(r)
		require.NoError(t, err)
		require.NoError(t, r.Close())
		assert.Equal(t, blobJSONLength(int64(len(content))), int64(len(body)))

		var parsed struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		require.NoError(t, json.Unmarshal(body, &parsed))
		assert.Equal(t, "base64", parsed.Encoding)
		decoded, err := base64.StdEncoding.DecodeString(parsed.Content)
		require.NoError(t, err)
		assert.Equal(t, content, decoded)
	}
}

func TestBlobJSONReadCloser_SmallReads(t *testing.T) {
	content := []byte("streaming-blob-payload")
	r := newBlobJSONReadCloser(io.NopCloser(strings.NewReader(string(content))))
	var got []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, r.Close())

	var parsed struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(got, &parsed))
	decoded, err := base64.StdEncoding.DecodeString(parsed.Content)
	require.NoError(t, err)
	assert.Equal(t, content, decoded)
}

func TestBlobJSONReadCloser_ReadError(t *testing.T) {
	r := newBlobJSONReadCloser(io.NopCloser(&errReader{err: io.ErrUnexpectedEOF}))
	_, err := io.ReadAll(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, r.Close())
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func bytesOf(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
