package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/th0rn0/thornotes-desktop-wails/internal/config"
)

// ── Load ─────────────────────────────────────────────────────────────────────

func TestLoad_FileNotExist(t *testing.T) {
	cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("Load(missing) error = %v, want nil", err)
	}
	if cfg.ServerURL != "" {
		t.Errorf("ServerURL = %q, want empty", cfg.ServerURL)
	}
}

func TestLoad_ValidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{"serverUrl":"http://localhost:8080"}`), 0o644)

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.ServerURL != "http://localhost:8080" {
		t.Errorf("ServerURL = %q, want %q", cfg.ServerURL, "http://localhost:8080")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`not valid json`), 0o644)

	_, err := config.Load(p)
	if err == nil {
		t.Error("Load(invalid JSON) error = nil, want error")
	}
}

func TestLoad_UnknownFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{"serverUrl":"http://localhost:8080","unknownField":42}`), 0o644)

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.ServerURL != "http://localhost:8080" {
		t.Errorf("ServerURL = %q, want %q", cfg.ServerURL, "http://localhost:8080")
	}
}

func TestLoad_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(""), 0o644)

	_, err := config.Load(p)
	if err == nil {
		t.Error("Load(empty file) error = nil, want JSON parse error")
	}
}

func TestLoad_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	_ = os.WriteFile(p, []byte(`{}`), 0o644)

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load({}) error = %v", err)
	}
	if cfg.ServerURL != "" {
		t.Errorf("ServerURL = %q, want empty", cfg.ServerURL)
	}
}

// ── Save ─────────────────────────────────────────────────────────────────────

func TestSave_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	original := config.AppConfig{ServerURL: "https://notes.example.com"}

	if err := config.Save(p, original); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	got, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load after Save error = %v", err)
	}
	if got.ServerURL != original.ServerURL {
		t.Errorf("ServerURL = %q, want %q", got.ServerURL, original.ServerURL)
	}
}

func TestSave_CreatesParentDirectories(t *testing.T) {
	p := filepath.Join(t.TempDir(), "deep", "nested", "dir", "config.json")

	if err := config.Save(p, config.AppConfig{ServerURL: "http://localhost:8080"}); err != nil {
		t.Fatalf("Save error = %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestSave_ProducesValidJSON(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(p, config.AppConfig{ServerURL: "http://localhost:8080"}); err != nil {
		t.Fatalf("Save error = %v", err)
	}

	data, _ := os.ReadFile(p)
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
}

func TestSave_OverwritesExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	_ = config.Save(p, config.AppConfig{ServerURL: "http://old.example.com"})
	_ = config.Save(p, config.AppConfig{ServerURL: "http://new.example.com"})

	cfg, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if cfg.ServerURL != "http://new.example.com" {
		t.Errorf("ServerURL = %q, want new URL", cfg.ServerURL)
	}
}

func TestSave_EmptyConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(p, config.AppConfig{}); err != nil {
		t.Fatalf("Save(empty) error = %v", err)
	}

	cfg, _ := config.Load(p)
	if cfg.ServerURL != "" {
		t.Errorf("ServerURL = %q, want empty", cfg.ServerURL)
	}
}
