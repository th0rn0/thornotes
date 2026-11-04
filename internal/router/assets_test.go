package router

import (
	"net/http"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mapFS builds a small static tree; each call returns an independent copy so a
// test can change one byte without disturbing the baseline.
func mapFS(appJS string, extra map[string]string) fstest.MapFS {
	m := fstest.MapFS{
		"js/app.js":     &fstest.MapFile{Data: []byte(appJS)},
		"css/app.css":   &fstest.MapFile{Data: []byte("body{color:#000}\n")},
		"icons/one.svg": &fstest.MapFile{Data: []byte("<svg/>\n")},
	}
	for name, data := range extra {
		m[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return m
}

// TestIndexAssets_VersionIsDeterministic guards the first half of the contract:
// two runs over identical bytes must produce the same version, or every restart
// would look like a deploy and evict everybody's cache.
func TestIndexAssets_VersionIsDeterministic(t *testing.T) {
	first, err := indexAssets(http.FS(mapFS("console.log(1);\n", nil)))
	require.NoError(t, err)

	second, err := indexAssets(http.FS(mapFS("console.log(1);\n", nil)))
	require.NoError(t, err)

	assert.Len(t, first.version, assetVersionHexLen)
	assert.Regexp(t, "^[0-9a-f]+$", first.version)
	assert.Equal(t, first.version, second.version)
}

// TestIndexAssets_VersionTracksStaticBytes is the regression that matters: the
// version must be derived from the embedded bytes, so editing one static file
// moves it even though no Go code and no release constant changed.
func TestIndexAssets_VersionTracksStaticBytes(t *testing.T) {
	base, err := indexAssets(http.FS(mapFS("console.log(1);\n", nil)))
	require.NoError(t, err)

	edited, err := indexAssets(http.FS(mapFS("console.log(2);\n", nil)))
	require.NoError(t, err)
	if base.version == edited.version {
		t.Fatalf("version did not change when a static byte changed: %s", base.version)
	}

	added, err := indexAssets(http.FS(mapFS("console.log(1);\n", map[string]string{
		"js/new-file.js": "// new\n",
	})))
	require.NoError(t, err)
	if base.version == added.version {
		t.Fatalf("version did not change when a static file was added: %s", base.version)
	}

	emptied, err := indexAssets(http.FS(mapFS("console.log(1);\n", map[string]string{
		"css/app.css": "", // emptied, not deleted: the length changes
	})))
	require.NoError(t, err)
	if base.version == emptied.version {
		t.Fatalf("version did not change when a static file was emptied: %s", base.version)
	}
}

// TestIndexAssets_PerFileETags asserts every served file gets its own validator
// — the embed FS has no mod times, so without this a no-cache URL would be
// re-downloaded in full on every reload.
func TestIndexAssets_PerFileETags(t *testing.T) {
	idx, err := indexAssets(http.FS(mapFS("console.log(1);\n", nil)))
	require.NoError(t, err)

	appETag, ok := idx.etags["/js/app.js"]
	require.True(t, ok, "no ETag for /js/app.js: %v", idx.etags)
	cssETag := idx.etags["/css/app.css"]
	assert.NotEmpty(t, cssETag)
	assert.NotEqual(t, appETag, cssETag)
	assert.Regexp(t, `^"[0-9a-f]{16}"$`, appETag)

	changed, err := indexAssets(http.FS(mapFS("console.log(2);\n", nil)))
	require.NoError(t, err)
	assert.NotEqual(t, appETag, changed.etags["/js/app.js"])
	assert.Equal(t, cssETag, changed.etags["/css/app.css"])
}

// TestCacheControlFor pins the header policy: only the exact current version may
// be held for a year, and an empty index (hashing failed at startup) must never
// qualify for the immutable path.
func TestCacheControlFor(t *testing.T) {
	idx := &assetIndex{version: "abcd1234"}
	assert.Equal(t, cacheControlImmutable, idx.cacheControlFor("abcd1234"))
	assert.Equal(t, cacheControlNoCache, idx.cacheControlFor(""))
	assert.Equal(t, cacheControlNoCache, idx.cacheControlFor("deadbeef"))
	assert.Equal(t, cacheControlNoCache, idx.cacheControlFor("abcd123"))

	empty := &assetIndex{}
	assert.Equal(t, cacheControlNoCache, empty.cacheControlFor(""))
	assert.Equal(t, cacheControlNoCache, empty.cacheControlFor("abcd1234"))
}
