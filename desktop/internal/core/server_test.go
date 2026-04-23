package core

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckServer_Up(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !CheckServer(srv.URL) {
		t.Error("CheckServer should return true for a running server")
	}
}

func TestCheckServer_Down(t *testing.T) {
	// Use a URL that refuses connections immediately.
	if CheckServer("http://127.0.0.1:1") {
		t.Error("CheckServer should return false for a non-listening port")
	}
}

func TestCheckServer_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if CheckServer(srv.URL) {
		t.Error("CheckServer should return false for a 500 response")
	}
}

func TestCheckServer_ClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	// 401 is a 4xx — server is up, just requires auth. Should return true.
	if !CheckServer(srv.URL) {
		t.Error("CheckServer should return true for 4xx (server is reachable)")
	}
}

func TestCheckServer_RedirectFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if !CheckServer(srv.URL) {
		t.Error("CheckServer should follow redirects and return true")
	}
}

func TestCheckServer_InvalidURL(t *testing.T) {
	if CheckServer("not-a-url") {
		t.Error("CheckServer should return false for an invalid URL")
	}
}
