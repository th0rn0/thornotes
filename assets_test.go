package thornotes

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// TestAppTemplate_SidebarCollapseArtifacts asserts that the app shell template
// renders and carries the markup + CSS needed for the desktop sidebar collapse
// feature. This is a belt-and-braces check against template regressions — if
// any of these are accidentally removed, the collapse toggle silently breaks.
func TestAppTemplate_SidebarCollapseArtifacts(t *testing.T) {
	tmpl, err := template.ParseFS(TemplatesFS, "web/templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "app.html", nil); err != nil {
		t.Fatalf("execute app.html: %v", err)
	}
	out := buf.String()

	cases := []struct {
		name   string
		needle string
	}{
		{"hamburger toggle button", `class="topbar-menu-btn"`},
		{"sidebar element id", `id="sidebar"`},
		{"collapsed-state CSS rule", `.sidebar.collapsed`},
		{"width transition on sidebar", `transition: width`},
		// Mobile override must reset the collapsed width so the drawer still opens.
		{"mobile override for collapsed", `.sidebar.collapsed { width: 220px`},
	}
	for _, tc := range cases {
		if !strings.Contains(out, tc.needle) {
			t.Errorf("app.html missing %s: expected substring %q", tc.name, tc.needle)
		}
	}
}

// TestAppTemplate_ShareTemplate_StillRenders is a smoke test: the share view
// template must still parse and render alongside app.html in the same FS. If
// the embed pattern ever stops picking both up, this fails.
func TestAppTemplate_ShareTemplate_StillRenders(t *testing.T) {
	tmpl, err := template.ParseFS(TemplatesFS, "web/templates/*.html")
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	// share.html expects a struct with Title, UpdatedAt, Content — pass a
	// minimal map-shaped stand-in to avoid importing the model package here.
	data := struct {
		Title     string
		UpdatedAt mockTime
		Content   string
	}{Title: "hi", UpdatedAt: mockTime{}, Content: ""}
	if err := tmpl.ExecuteTemplate(new(bytes.Buffer), "share.html", data); err != nil {
		t.Fatalf("execute share.html: %v", err)
	}
}

type mockTime struct{}

func (mockTime) Format(_ string) string { return "Jan 1, 2026" }
