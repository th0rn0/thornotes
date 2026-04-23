package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func autostartPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", "thornotes-desktop.desktop"), nil
}

func setAutostart(enabled bool) error {
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !enabled {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	contents := fmt.Sprintf("[Desktop Entry]\nType=Application\nName=Thornotes\nExec=%s\nIcon=thornotes-desktop\nX-GNOME-Autostart-enabled=true\n", exe)
	return os.WriteFile(path, []byte(contents), 0o644)
}
