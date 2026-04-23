package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutostartPath_UsesHomeDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := AutostartPath()
	if err != nil {
		t.Fatalf("AutostartPath: %v", err)
	}
	want := filepath.Join(tmp, ".config", "autostart", "thornotes-desktop.desktop")
	if got != want {
		t.Errorf("AutostartPath = %q, want %q", got, want)
	}
}

func TestSetAutostart_Enable_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := SetAutostart(true); err != nil {
		t.Fatalf("SetAutostart(true): %v", err)
	}

	path, _ := AutostartPath()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read autostart file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "[Desktop Entry]") {
		t.Error("missing [Desktop Entry] header")
	}
	if !strings.Contains(content, "Name=Thornotes") {
		t.Error("missing Name=Thornotes")
	}
	if !strings.Contains(content, "Type=Application") {
		t.Error("missing Type=Application")
	}
	if !strings.Contains(content, "X-GNOME-Autostart-enabled=true") {
		t.Error("missing X-GNOME-Autostart-enabled=true")
	}
	if !strings.Contains(content, "Exec=") {
		t.Error("missing Exec= line")
	}
}

func TestSetAutostart_Enable_CreatesDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := SetAutostart(true); err != nil {
		t.Fatalf("SetAutostart(true): %v", err)
	}

	dir := filepath.Join(tmp, ".config", "autostart")
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("autostart directory not created: %v", err)
	}
}

func TestSetAutostart_Disable_RemovesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Enable first
	if err := SetAutostart(true); err != nil {
		t.Fatal(err)
	}
	path, _ := AutostartPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("file should exist after enable")
	}

	// Now disable
	if err := SetAutostart(false); err != nil {
		t.Fatalf("SetAutostart(false): %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be removed after disable")
	}
}

func TestSetAutostart_DisableIdempotent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Disable when no file exists — should be a no-op
	if err := SetAutostart(false); err != nil {
		t.Errorf("SetAutostart(false) on non-existent file: %v", err)
	}
}

func TestSetAutostart_MkdirAllFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Put a file where .config/autostart/ directory should be created.
	autostartParent := filepath.Join(tmp, ".config", "autostart")
	// Create .config/ as a directory, then put a file named "autostart" inside it.
	if err := os.MkdirAll(filepath.Join(tmp, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(autostartParent, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := SetAutostart(true)
	if err == nil {
		t.Error("expected error when autostart dir cannot be created")
	}
}

func TestSetAutostart_ExecContainsBinaryPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := SetAutostart(true); err != nil {
		t.Fatal(err)
	}

	path, _ := AutostartPath()
	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	var execLine string
	for _, l := range lines {
		if strings.HasPrefix(l, "Exec=") {
			execLine = l
			break
		}
	}
	if execLine == "" {
		t.Fatal("no Exec= line found")
	}
	// The exec path should be a non-empty absolute path
	execVal := strings.TrimPrefix(execLine, "Exec=")
	if execVal == "" {
		t.Error("Exec= value is empty")
	}
	if !filepath.IsAbs(execVal) {
		t.Errorf("Exec= value %q is not an absolute path", execVal)
	}
}
