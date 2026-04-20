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
//
// Coverage:
//   - The collapse button lives in the sidebar footer (not the topbar) and
//     has a stable id the JS binds to.
//   - The collapsed state is a thin rail (width: 36px), NOT width: 0 — that
//     rail is what keeps the toggle reachable when collapsed.
//   - Every in-sidebar section the rail should hide is listed in the
//     .collapsed selector, so nothing bleeds through.
//   - The mobile media query overrides .sidebar.collapsed so the off-canvas
//     drawer still works on small screens.
//   - The topbar hamburger exists (needed for mobile) but stays hidden on
//     desktop so the thornotes logo sits flush-left as before.
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
		// Markup — the header button with the id the JS binds to.
		{"sidebar collapse button id", `id="sidebar-collapse-btn"`},
		{"sidebar collapse button class", `class="sidebar-collapse-btn"`},
		{"sidebar element id", `id="sidebar"`},
		// The collapse button sits in a dedicated header row at the very top
		// of the sidebar — NOT inside the footer alongside the GitHub link.
		{"sidebar-header wraps the button", `<div class="sidebar-header">`},
		// The topbar hamburger is still in the DOM (mobile needs it) but its
		// default display must be 'none' so the desktop topbar looks as it
		// did before the collapse feature was added.
		{"topbar hamburger still present", `class="topbar-menu-btn"`},
		// The default block for .topbar-menu-btn must start with display: none
		// so the desktop topbar doesn't get an unnecessary button.
		{"topbar hamburger hidden by default", ".topbar-menu-btn {\n  display: none;"},
		// Core CSS for the rail-collapse behavior.
		{"collapsed-state CSS rule", `.sidebar.collapsed`},
		{"collapsed rail width (36px, NOT 0)", `.sidebar.collapsed { width: 36px`},
		{"width transition on sidebar", `transition: width`},
		// Sections hidden in rail mode.
		{"rail hides sidebar-toolbar", `.sidebar.collapsed .sidebar-toolbar`},
		{"rail hides tree", `.sidebar.collapsed .tree`},
		{"rail hides sidebar-footer (incl. github link)", `.sidebar.collapsed .sidebar-footer`},
		// The header stays visible in the rail so the toggle remains reachable.
		{"rail keeps header visible", `.sidebar.collapsed .sidebar-header`},
		// Mobile override — rail CSS doesn't apply on phones.
		{"mobile override keeps full sidebar", `.sidebar.collapsed { width: 220px`},
		{"mobile override hides header collapse btn", `.sidebar-collapse-btn { display: none; }`},
	}
	for _, tc := range cases {
		if !strings.Contains(out, tc.needle) {
			t.Errorf("app.html missing %s: expected substring %q", tc.name, tc.needle)
		}
	}
}

// TestAppStaticJS_CollapseWiring asserts that the static JS file carries the
// functions and bindings the collapse feature depends on. Full behavioral
// coverage needs a JS runner; until that exists, this catches the most likely
// regression: someone removes the toggle, the binding, or the localStorage
// persistence.
func TestAppStaticJS_CollapseWiring(t *testing.T) {
	data, err := StaticFS.ReadFile("web/static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	src := string(data)

	cases := []struct {
		name   string
		needle string
	}{
		{"toggleSidebarCollapse function defined", `function toggleSidebarCollapse`},
		{"syncSidebarCollapseBtn function defined", `function syncSidebarCollapseBtn`},
		{"restoreSidebarCollapse function defined", `function restoreSidebarCollapse`},
		{"collapse state persisted in localStorage", `localStorage.setItem('sidebarCollapsed'`},
		{"collapse state read from localStorage", `localStorage.getItem('sidebarCollapsed')`},
		{"footer button bound to toggle", `getElementById('sidebar-collapse-btn')`},
		{"showApp restores saved state", `restoreSidebarCollapse()`},
		// Mobile hamburger still drives the off-canvas drawer, not the collapse.
		{"mobile hamburger bound to toggleSidebar", `.topbar-menu-btn').addEventListener('click', toggleSidebar)`},
	}
	for _, tc := range cases {
		if !strings.Contains(src, tc.needle) {
			t.Errorf("app.js missing %s: expected substring %q", tc.name, tc.needle)
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
