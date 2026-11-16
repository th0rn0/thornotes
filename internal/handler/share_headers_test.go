package handler_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestShareView_SetsNoStoreAndNoIndex pins the two headers ticket 601 asks for on
// the public share view.
//
// Why both, and why on every outcome: a share URL is a bearer capability — whoever
// holds it reads the note with no session and no account. `Cache-Control: no-store`
// stops anything between the instance and the reader from keeping a copy (a proxy
// that caches this serves the note to whoever asks next), and `X-Robots-Tag:
// noindex` keeps a pasted link out of a search index. The 404 branch is asserted
// too, because a cacheable 404 is the same disclosure in the other direction: it
// tells a cache that a burned token is worth remembering.
func TestShareView_SetsNoStoreAndNoIndex(t *testing.T) {
	f := newIsolationFixture(t)
	f.asAlice(t)

	resp := f.client.do(t, http.MethodPost, "/api/v1/notes/"+i64str(f.noteID)+"/share", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var share map[string]any
	decodeInto(t, resp, &share)
	token, ok := share["share_token"].(string)
	require.True(t, ok, "sharing must return a token")
	require.NotEmpty(t, token)

	t.Run("live link", func(t *testing.T) {
		resp := f.client.do(t, http.MethodGet, "/s/"+token, nil)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
	})

	t.Run("unknown token", func(t *testing.T) {
		resp := f.client.do(t, http.MethodGet, "/s/00000000000000000000000000000000", nil)
		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		assert.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		assert.Equal(t, "noindex", resp.Header.Get("X-Robots-Tag"))
	})
}
