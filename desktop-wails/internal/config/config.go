package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// AppConfig is the persisted desktop configuration.
type AppConfig struct {
	ServerURL string `json:"serverUrl"`
}

// Load reads AppConfig from path.
// If the file does not exist, an empty AppConfig is returned with no error.
// Any other read or parse error is returned to the caller.
func Load(path string) (AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AppConfig{}, nil
		}
		return AppConfig{}, err
	}
	var cfg AppConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AppConfig{}, err
	}
	return cfg, nil
}

// Save persists cfg to path, creating any missing parent directories.
func Save(path string, cfg AppConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
