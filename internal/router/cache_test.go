package router_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The asset version the shell pins on its script tag. Reading it back out of the
// shell — rather than calling a helper — keeps these tests honest about what a
// browser actually receives.
var shellAssetVersionRe = regexp.MustCompile(`/static/js/app\.js\?v=([0-9a-f]{8})`)

func serve(h http.Handler, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func shellAssetVersion(t *testing.T, h http.Handler) string {
	t.Helper()
	rr := serve(h, "/")
	require.Equal(t, http.StatusOK, rr.Code)
	m := shellAssetVersionRe.FindStringSubmatch(rr.Body.String())
	require.NotNil(t, m, "app shell does not pin a versioned app.js URL")
	return m[1]
}

// TestRouter_AppShell_NoCache: the shell is what pins the asset URLs, so it must
// never be held by the browser or the proxy. Without this the old shell keeps
// asking for the old (versioned) assets and nothing changes on deploy.
func TestRouter_AppShell_NoCache(t *testing.T) {
	h := buildHandler(t)

	rr := serve(h, "/")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Regexp(t, shellAssetVersionRe, rr.Body.String())
}

// TestRouter_DeepLink_NoCache: the SPA fallback serves the same shell, so it
// carries the same policy.
func TestRouter_DeepLink_NoCache(t *testing.T) {
	h := buildHandler(t)

	rr := serve(h, "/some/folder/some-note")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Regexp(t, shellAssetVersionRe, rr.Body.String())
}

// TestRouter_Static_CurrentVersionIsImmutable: the URL now identifies the bytes,
// so a year-long cache can never serve a stale body.
func TestRouter_Static_CurrentVersionIsImmutable(t *testing.T) {
	h := buildHandler(t)
	version := shellAssetVersion(t, h)

	rr := serve(h, "/static/js/app.js?v="+version)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "public, max-age=31536000, immutable", rr.Header().Get("Cache-Control"))
	assert.NotEmpty(t, rr.Header().Get("ETag"))
	assert.Contains(t, rr.Body.String(), "thornotes")
}

// TestRouter_Static_UnversionedIsNoCache: an unversioned URL is exactly what a
// proxy's injected max-age would pin, so it must not be cacheable.
func TestRouter_Static_UnversionedIsNoCache(t *testing.T) {
	h := buildHandler(t)

	rr := serve(h, "/static/js/app.js")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.NotEmpty(t, rr.Header().Get("ETag"))
	assert.Contains(t, rr.Body.String(), "thornotes")
}

// TestRouter_Static_StaleVersionIsNoCache: an old shell asks for an old version;
// those bytes must not be pinned for a year.
func TestRouter_Static_StaleVersionIsNoCache(t *testing.T) {
	h := buildHandler(t)
	version := shellAssetVersion(t, h)

	stale := "00000000"
	if stale == version {
		stale = "11111111"
	}
	rr := serve(h, "/static/js/app.js?v="+stale)
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
}

// TestRouter_Static_RevalidatesWithETag documents the validator: the embed FS
// has no mod times, so the ETag is the only thing that can turn a reload into a
// 304 instead of a full download.
func TestRouter_Static_RevalidatesWithETag(t *testing.T) {
	h := buildHandler(t)
	version := shellAssetVersion(t, h)

	first := serve(h, "/static/css/font-awesome.min.css?v="+version)
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req := httptest.NewRequest(http.MethodGet, "/static/css/font-awesome.min.css?v="+version, nil)
	req.Header.Set("If-None-Match", etag)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotModified, rr.Code)
	assert.Empty(t, rr.Body.String())
}

// TestRouter_Static_DirectoryListingNotServed: the static tree is addressed by
// file only; a listing would expose the embedded test files and gains nothing.
func TestRouter_Static_DirectoryListingNotServed(t *testing.T) {
	h := buildHandler(t)

	rr := serve(h, "/static/js/")
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestRouter_ServiceWorker_CarriesAssetVersion is the load-bearing one. If the
// served sw.js bytes do not change when the assets change, the browser never
// reinstalls the worker and every other part of this fix is inert.
func TestRouter_ServiceWorker_CarriesAssetVersion(t *testing.T) {
	h := buildHandler(t)
	version := shellAssetVersion(t, h)

	rr := serve(h, "/sw.js")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "no-cache", rr.Header().Get("Cache-Control"))
	assert.Equal(t, "/", rr.Header().Get("Service-Worker-Allowed"))
	assert.Contains(t, rr.Header().Get("Content-Type"), "javascript")

	body := rr.Body.String()
	assert.NotContains(t, body, "__ASSET_VERSION__", "placeholder left in the served worker")
	assert.Contains(t, body, "const ASSET_VERSION = '"+version+"';",
		"served worker bytes do not carry the asset version — the browser would never reinstall it")
	assert.Contains(t, body, "const CACHE = cacheNameFor(ASSET_VERSION);",
		"cache name is not derived from the asset version")
	assert.Contains(t, body, "assetUrl('/static/js/app.js')",
		"app.js is not precached through a versioned URL")
	// The shell must not be precached: it is what pins the asset URLs, so it has
	// to come from the network (a bare '/' list entry is the old bug).
	assert.NotContains(t, body, "assetUrl('/')")
	assert.NotRegexp(t, regexp.MustCompile(`(?m)^\s*'/',\s*$`), body)
}
