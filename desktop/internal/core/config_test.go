package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigPath_UsesXDGDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	want := filepath.Join(tmp, "thornotes", "desktop.json")
	if got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
}

func TestLoadConfig_NoFile_ReturnsDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg, exists, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if exists {
		t.Error("exists should be false when no config file")
	}
	if cfg.ServerURL != "http://localhost:8080" {
		t.Errorf("default ServerURL = %q, want %q", cfg.ServerURL, "http://localhost:8080")
	}
	if cfg.Autostart {
		t.Error("default Autostart should be false")
	}
}

func TestLoadConfig_ExistingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "thornotes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"server_url":"http://notes.example.com:9090","autostart":true}`
	if err := os.WriteFile(filepath.Join(dir, "desktop.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, exists, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !exists {
		t.Error("exists should be true when config file is present")
	}
	if cfg.ServerURL != "http://notes.example.com:9090" {
		t.Errorf("ServerURL = %q", cfg.ServerURL)
	}
	if !cfg.Autostart {
		t.Error("Autostart should be true")
	}
}

func TestLoadConfig_UnknownFieldsIgnored(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "thornotes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{"server_url":"http://localhost:8080","autostart":false,"future_field":"ignored"}`
	if err := os.WriteFile(filepath.Join(dir, "desktop.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig with unknown fields should not error: %v", err)
	}
}

func TestLoadConfig_CorruptFile_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "thornotes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "desktop.json"), []byte("not json{{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadConfig()
	if err == nil {
		t.Error("expected error for corrupt JSON")
	}
}

func TestSaveConfig_WritesAndReloads(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	want := &Config{ServerURL: "http://my-server:1234", Autostart: true}
	if err := SaveConfig(want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	got, exists, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig after save: %v", err)
	}
	if !exists {
		t.Error("exists should be true after save")
	}
	if got.ServerURL != want.ServerURL {
		t.Errorf("ServerURL = %q, want %q", got.ServerURL, want.ServerURL)
	}
	if got.Autostart != want.Autostart {
		t.Errorf("Autostart = %v, want %v", got.Autostart, want.Autostart)
	}
}

func TestSaveConfig_CreatesDirectories(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "nested", "dir"))

	if err := SaveConfig(&Config{ServerURL: "http://localhost:8080"}); err != nil {
		t.Fatalf("SaveConfig with non-existent dirs: %v", err)
	}
	path, _ := ConfigPath()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestSaveConfig_FilePermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := SaveConfig(&Config{ServerURL: "http://localhost:8080"}); err != nil {
		t.Fatal(err)
	}
	path, _ := ConfigPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config file permissions = %o, want 0600", perm)
	}
}

func TestSaveConfig_IsValidJSON(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := SaveConfig(&Config{ServerURL: "http://localhost:8080", Autostart: false}); err != nil {
		t.Fatal(err)
	}
	path, _ := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Errorf("saved file is not valid JSON: %v", err)
	}
}

func TestSaveConfig_Overwrite(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	if err := SaveConfig(&Config{ServerURL: "http://first:8080"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(&Config{ServerURL: "http://second:9090", Autostart: true}); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "http://second:9090" {
		t.Errorf("ServerURL after overwrite = %q", cfg.ServerURL)
	}
}

func TestSaveConfig_MkdirAllFails(t *testing.T) {
	tmp := t.TempDir()
	// Put a regular file where the thornotes/ directory should be created.
	// MkdirAll will fail because it can't mkdir over a file.
	if err := os.WriteFile(filepath.Join(tmp, "thornotes"), []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", tmp)

	err := SaveConfig(&Config{ServerURL: "http://localhost:8080"})
	if err == nil {
		t.Error("expected error when config dir cannot be created")
	}
}

func TestSaveConfig_WriteFileFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test write-permission error as root")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	// Create the config directory and then make it read-only.
	dir := filepath.Join(tmp, "thornotes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := SaveConfig(&Config{ServerURL: "http://localhost:8080"})
	if err == nil {
		t.Error("expected error when config dir is read-only")
	}
}

func TestLoadConfig_PermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test read-permission error as root")
	}
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	dir := filepath.Join(tmp, "thornotes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "desktop.json")
	if err := os.WriteFile(path, []byte(`{"server_url":"http://localhost:8080"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, _, err := LoadConfig()
	if err == nil {
		t.Error("expected error for unreadable config file")
	}
}
