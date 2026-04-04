package config

import (
	"errors"
	"net/url"
	"strings"
)

// ValidateServerURL trims whitespace, strips trailing slashes, and checks that
// the URL is a valid http/https address with a non-empty host.
// Returns the normalised URL or a user-facing error.
func ValidateServerURL(raw string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	if s == "" {
		return "", errors.New("Server URL is required.")
	}
	u, err := url.ParseRequestURI(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("URL must start with http:// or https://")
	}
	return s, nil
}
