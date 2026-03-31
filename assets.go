// Package thornotes exports the embedded web assets (templates and static files).
package thornotes

import "embed"

//go:embed web/templates/*.html
var TemplatesFS embed.FS

//go:embed web/static
var StaticFS embed.FS
