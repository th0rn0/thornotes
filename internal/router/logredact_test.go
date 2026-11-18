package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// TestRedactLogPath pins the shape of the redaction: the token becomes a short
// stable handle, everything else is untouched.
//
// The three properties that matter are all here: the token does not survive, the
// same token still produces the same handle (so an operator can count and trace
// one link), and two different tokens do not collide.
func TestRedactLogPath(t *testing.T) {
	const token = "9f2c1a4b6d8e0f3a5b7c9d1e3f5a7b9c"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"share link is redacted", "/s/" + token, redactLogPath("/s/" + token)},
		{"non-share path untouched", "/api/v1/notes/42", "/api/v1/notes/42"},
		{"root untouched", "/", "/"},
		{"prefix without a token is left alone", "/s/", "/s/"},
		{"a path that merely starts with s is untouched", "/settings", "/settings"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactLogPath(tc.in)
			if got != tc.want {
				t.Fatalf("redactLogPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if tc.in != tc.want && strings.Contains(got, token) {
				t.Fatalf("redacted path still contains the token: %q", got)
			}
		})
	}

	// Stable: the same token is the same handle across calls.
	first := redactLogPath("/s/" + token)
	second := redactLogPath("/s/" + token)
	if first != second {
		t.Fatalf("redaction is not stable: %q then %q", first, second)
	}

	// Distinct: two tokens do not share a handle.
	other := redactLogPath("/s/" + token[:31] + "d")
	if other == first {
		t.Fatalf("two different tokens collapsed to the same handle %q", first)
	}

	// Short enough to read in a log, long enough not to collide in a vault.
	if len(first) > len("/s/t_")+8 {
		t.Errorf("handle %q is longer than expected", first)
	}
}

// TestAccessLog_DoesNotContainShareToken is the acceptance this ticket actually
// asked for: a working share capability must not reach the access log.
//
// It runs the real middleware over a real request rather than calling the helper,
// because the defect was the middleware's choice of field — a correct helper that
// nothing calls proves nothing.
func TestAccessLog_DoesNotContainShareToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var buf bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&buf)
	t.Cleanup(func() { log.Logger = previous })

	const token = "0a1b2c3d4e5f60718293a4b5c6d7e8f9"

	engine := gin.New()
	engine.Use(zerologAccessMiddleware())
	engine.GET("/s/:token", func(c *gin.Context) { c.Status(http.StatusOK) })
	// The second public prefix (ticket 616). Without it in this test the folder
	// surface is only covered by the handler-level test, which cannot pin the
	// middleware's choice of field on its own.
	engine.GET("/f/:token", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/f/:token/n/*path", func(c *gin.Context) { c.Status(http.StatusOK) })
	engine.GET("/api/v1/notes/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/s/"+token, nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, w.Code)
		}
	}

	// The folder link, and a deep link under it.
	for _, path := range []string{"/f/" + token, "/f/" + token + "/n/projects/plan"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}

	// A non-share request in the same log, to prove the field was not dropped
	// wholesale — the log still has to be useful.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notes/42", nil)
	engine.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	if out == "" {
		t.Fatal("the access middleware logged nothing; the capture is broken, not the code")
	}
	if strings.Contains(out, token) {
		t.Fatalf("the access log contains the share token:\n%s", out)
	}
	if !strings.Contains(out, `"path":"/api/v1/notes/42"`) {
		t.Fatalf("non-share paths must still be logged verbatim:\n%s", out)
	}

	// Both share requests carry the same redacted handle, so the log can still be
	// grouped by link.
	handle := redactLogPath("/s/" + token)
	if got := strings.Count(out, `"path":"`+handle+`"`); got != 2 {
		t.Fatalf("expected both share requests under handle %q, found %d:\n%s", handle, got, out)
	}

	// The folder link is redacted under its own prefix, and its deep link keeps
	// the sub-path so a note read is still attributable to the link it came
	// through.
	folderHandle := redactLogPath("/f/" + token)
	if got := strings.Count(out, `"path":"`+folderHandle+`"`); got != 1 {
		t.Fatalf("expected the folder request under handle %q, found %d:\n%s", folderHandle, got, out)
	}
	if !strings.Contains(out, `"path":"`+folderHandle+`/n/projects/plan"`) {
		t.Fatalf("the folder deep link must keep its sub-path under the redacted handle:\n%s", out)
	}
}

// TestRedactLogPath_FolderShare pins the second public prefix, added by ticket
// 616. It is a separate test rather than one more table row because the property
// that is easy to get wrong is the *split*: the token is the first segment only,
// and the rest of the path survives.
func TestRedactLogPath_FolderShare(t *testing.T) {
	const token = "9f2c1a4b6d8e0f3a5b7c9d1e3f5a7b9c"

	root := redactLogPath("/f/" + token)
	if !strings.HasPrefix(root, "/f/t_") {
		t.Fatalf("a folder share path must be redacted under /f/, got %q", root)
	}
	if strings.Contains(root, token) {
		t.Fatalf("redacted path still contains the token: %q", root)
	}
	if strings.Contains(root, "/s/") {
		t.Fatalf("the folder prefix must not be rewritten into the note prefix: %q", root)
	}

	deep := redactLogPath("/f/" + token + "/n/projects/plan")
	if deep != root+"/n/projects/plan" {
		t.Fatalf("a deep link must keep its sub-path after the redacted token: got %q, want %q",
			deep, root+"/n/projects/plan")
	}
	if strings.Contains(deep, token) {
		t.Fatalf("redacted deep link still contains the token: %q", deep)
	}

	// The same token under the two prefixes is two capabilities, so it must not
	// collapse into one handle.
	if redactLogPath("/s/"+token) == root {
		t.Fatalf("note and folder handles must be distinguishable")
	}

	// Non-share paths stay verbatim, including ones that merely start with "f".
	for _, path := range []string{"/folders/12", "/f/", "/f", "/api/v1/folders/1/share", "/"} {
		if got := redactLogPath(path); got != path {
			t.Fatalf("redactLogPath(%q) = %q, want it untouched", path, got)
		}
	}
}
