package config_test

import (
	"testing"

	"github.com/th0rn0/thornotes-desktop-wails/internal/config"
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantURL string
		wantErr bool
	}{
		// Happy paths
		{"http with port", "http://localhost:8080", "http://localhost:8080", false},
		{"https URL", "https://notes.example.com", "https://notes.example.com", false},
		{"with sub-path", "http://example.com/thornotes", "http://example.com/thornotes", false},
		{"IP address", "http://192.168.1.42:3000", "http://192.168.1.42:3000", false},
		{"subdomain", "https://notes.myserver.io", "https://notes.myserver.io", false},
		// Normalisation
		{"trailing slash stripped", "http://localhost:8080/", "http://localhost:8080", false},
		{"multiple trailing slashes", "http://localhost:8080///", "http://localhost:8080", false},
		{"leading whitespace trimmed", "  http://localhost:8080", "http://localhost:8080", false},
		{"trailing whitespace trimmed", "http://localhost:8080  ", "http://localhost:8080", false},
		{"whitespace and slash", "  http://localhost:8080/  ", "http://localhost:8080", false},
		// Empty / null-like
		{"empty string", "", "", true},
		{"whitespace only", "   ", "", true},
		// Bad schemes
		{"ftp scheme", "ftp://files.example.com", "", true},
		{"file scheme", "file:///home/user/notes", "", true},
		{"ws scheme", "ws://localhost:8080", "", true},
		// Malformed
		{"bare hostname", "localhost:8080", "", true},
		{"no scheme", "localhost", "", true},
		{"double slash only", "//", "", true},
		{"gibberish", "not a url at all!!!", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := config.ValidateServerURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServerURL(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantURL {
				t.Errorf("ValidateServerURL(%q) = %q, want %q", tt.input, got, tt.wantURL)
			}
		})
	}
}
