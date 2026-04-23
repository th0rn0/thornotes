package core

import (
	"net/http"
	"time"
)

// CheckServer returns true if serverURL responds with a non-5xx status within 5 s.
func CheckServer(serverURL string) bool {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(serverURL) //nolint:noctx
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}
