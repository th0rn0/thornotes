package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html/template"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

const (
	// assetVersionParam is the query parameter that carries the asset version
	// on every static URL the app shell emits.
	assetVersionParam = "v"

	// assetVersionPlaceholder is substituted with the current asset version
	// when the service worker is served. A browser only re-runs a service
	// worker's install step when the script's bytes change, so substituting a
	// value derived from the embedded assets is what makes a deploy reach
	// Cache Storage at all. Without it the fix is inert.
	assetVersionPlaceholder = "__ASSET_VERSION__"

	// assetVersionHexLen is the length of the asset version: short enough for a
	// URL, wide enough that two asset sets do not collide in practice.
	assetVersionHexLen = 8

	// cacheControlImmutable is safe only for a versioned URL: the bytes and the
	// URL move together, so a stale body cannot sit behind a live URL.
	cacheControlImmutable = "public, max-age=31536000, immutable"

	// cacheControlNoCache makes the browser revalidate an unversioned URL
	// instead of pinning it for a proxy-injected max-age.
	cacheControlNoCache = "no-cache"
)

// assetIndex is the startup snapshot of the embedded static tree: one digest
// for the whole tree (the asset version that goes into URLs) and one digest
// per file (its ETag).
//
// Snapshotting once at startup is what keeps the served bytes and the emitted
// URLs in step — a few hundred KB of hashing is free and happens before the
// listener opens.
type assetIndex struct {
	version string            // 8-hex digest of the whole tree
	etags   map[string]string // FS path ("/js/app.js") -> quoted ETag
}

// indexAssets walks an http.FileSystem and derives a version from the file
// names and contents (length-prefixed, in sorted order), so adding, removing,
// renaming or editing any file moves the version. It deliberately does not
// consult the binary's release constant: an asset-only change must still
// produce a new version.
func indexAssets(fsys http.FileSystem) (*assetIndex, error) {
	tree := sha256.New()
	idx := &assetIndex{etags: make(map[string]string)}

	var walk func(dir string) error
	walk = func(dir string) error {
		f, err := fsys.Open(dir)
		if err != nil {
			return err
		}
		entries, err := f.Readdir(-1)
		_ = f.Close()
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, entry := range entries {
			p := path.Join(dir, entry.Name())
			if entry.IsDir() {
				if err := walk(p); err != nil {
					return err
				}
				continue
			}
			data, err := readFSFile(fsys, p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			idx.etags[p] = `"` + hex.EncodeToString(sum[:8]) + `"`

			// Name, length and bytes: all three so a rename or a truncating
			// edit cannot coincidentally reproduce the previous digest.
			_, _ = tree.Write([]byte(p))
			_, _ = tree.Write([]byte{0})
			_, _ = tree.Write([]byte(strconv.Itoa(len(data))))
			_, _ = tree.Write([]byte{0})
			_, _ = tree.Write(data)
		}
		return nil
	}

	if err := walk("/"); err != nil {
		return nil, err
	}
	idx.version = hex.EncodeToString(tree.Sum(nil))[:assetVersionHexLen]
	return idx, nil
}

// readFSFile opens name and reads it whole. Static assets are small and are
// read at most once per request (plus once at startup), so streaming buys
// nothing.
func readFSFile(fsys http.FileSystem, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// cacheControlFor decides how a static response may be cached.
//
// Only the current version may be held for a year: the URL changes when the
// bytes do, so nothing stale can hide behind it. A missing or stale version
// gets no-cache, which forces revalidation — this is the case an old shell or
// a proxy-injected max-age would otherwise pin forever.
func (idx *assetIndex) cacheControlFor(requested string) string {
	if requested != "" && requested == idx.version {
		return cacheControlImmutable
	}
	return cacheControlNoCache
}

// serveStatic serves one file out of the embedded static tree.
func (idx *assetIndex) serveStatic(fsys http.FileSystem) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := path.Clean("/" + strings.TrimPrefix(c.Param("filepath"), "/"))
		c.Header("Cache-Control", idx.cacheControlFor(c.Query(assetVersionParam)))

		f, err := fsys.Open(name)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		defer func() { _ = f.Close() }()

		info, err := f.Stat()
		if err != nil || info.IsDir() {
			// No directory listings: the tree is only ever addressed by file.
			c.Status(http.StatusNotFound)
			return
		}
		if etag, ok := idx.etags[name]; ok {
			c.Header("ETag", etag)
		}

		// ServeContent sets Content-Type from the extension, handles Range and
		// answers If-None-Match from the ETag above with a 304. The embed FS
		// carries a zero mod time, so no Last-Modified is sent — the ETag is the
		// only validator, and it exists because indexAssets hashed the bytes.
		http.ServeContent(c.Writer, c.Request, name, time.Time{}, f)
	}
}

// serveServiceWorker serves sw.js with the asset version substituted into its
// bytes, so the script changes on every deploy and the browser reinstalls it.
func (idx *assetIndex) serveServiceWorker(fsys http.FileSystem) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := readFSFile(fsys, "/sw.js")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		body := bytes.ReplaceAll(raw, []byte(assetVersionPlaceholder), []byte(idx.version))
		sum := sha256.Sum256(body)

		c.Header("Cache-Control", cacheControlNoCache)
		c.Header("Service-Worker-Allowed", "/")
		c.Header("Content-Type", "text/javascript; charset=utf-8")
		c.Header("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
		http.ServeContent(c.Writer, c.Request, "sw.js", time.Time{}, bytes.NewReader(body))
	}
}

// appShellData is the shell template's entire data model. The shell is the one
// document that pins the asset URLs, so it is the one place the version is
// injected.
type appShellData struct {
	AssetVersion string
}

// serveAppShell renders the SPA shell, uncached, with the current asset
// version. The shell is small and it is what pins the asset URLs, so it must
// never be held by a proxy for longer than one request.
func (idx *assetIndex) serveAppShell(tmpl *template.Template) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Header("Cache-Control", cacheControlNoCache)
		// Gin pre-sets 404 on the NoRoute path (deep links serve the shell from
		// there); the shell is a 200 either way.
		c.Status(http.StatusOK)
		if err := tmpl.ExecuteTemplate(c.Writer, "app.html", appShellData{AssetVersion: idx.version}); err != nil {
			log.Error().Err(err).Msg("execute app template")
		}
	}
}
