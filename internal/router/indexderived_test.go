package router

import "testing"

// TestIndexDerivedPath pins which routes are refused while the startup reconcile
// is still rebuilding the index.
//
// The function is a whitelist of the routes that are *not* index-derived, so the
// table has to include both halves: the paths that must be gated (a listing served
// from a half-built index is a wrong answer, and on an anonymous link a wrong
// answer is worse than a 503) and the paths that must not be (blocking login for
// the length of the pass would be a bigger outage than the one being fixed).
//
// /f/ joins /s/ here (ticket 616): a shared folder listing is read entirely from
// the folder and note tables the reconcile is filling.
func TestIndexDerivedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Gated: they read the note index.
		{"/s/abcdef", true},
		{"/f/abcdef", true},
		{"/f/abcdef/d/projects", true},
		{"/f/abcdef/n/projects/plan", true},
		{"/api/v1/notes", true},
		{"/api/v1/folders/1/notes", true},
		{"/mcp", true},

		// Not gated: sessions, users and embedded bytes only.
		{"/api/v1/csrf", false},
		{"/api/v1/auth/login", false},
		{"/api/v1/auth/config", false},
		{"/", false},
		{"/static/js/app.js", false},
		{"/sw.js", false},

		// Paths that merely start with the same letter must not be caught.
		{"/folders/12", false},
		{"/f", false},
		// The bare prefix is still the share surface (a tokenless request to it);
		// gating it costs nothing and keeps the rule a prefix rule.
		{"/f/", true},
		{"/s/", true},
		{"/settings", false},
	}

	for _, tc := range cases {
		if got := indexDerivedPath(tc.path); got != tc.want {
			t.Errorf("indexDerivedPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
