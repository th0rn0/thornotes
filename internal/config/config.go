package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
)

type Config struct {
	Addr              string
	DBPath            string
	NotesRoot         string
	AllowRegistration bool
	TrustedProxy      *net.IPNet // nil means trust nothing (direct connections only)
	MaxContentBytes   int64
}

func Parse() (*Config, error) {
	cfg := &Config{}
	var trustedProxy string

	flag.StringVar(&cfg.Addr, "addr", envOr("THORNOTES_ADDR", ":8080"), "listen address")
	flag.StringVar(&cfg.DBPath, "db", envOr("THORNOTES_DB", "thornotes.db"), "SQLite database path")
	flag.StringVar(&cfg.NotesRoot, "notes-root", envOr("THORNOTES_NOTES_ROOT", "notes"), "root directory for note files")
	flag.BoolVar(&cfg.AllowRegistration, "allow-registration", envBool("THORNOTES_ALLOW_REGISTRATION", true), "allow new user registration")
	flag.StringVar(&trustedProxy, "trusted-proxy", envOr("THORNOTES_TRUSTED_PROXY", ""), "CIDR of trusted reverse proxy (e.g. 10.0.0.0/8)")
	flag.Parse()

	cfg.MaxContentBytes = 1 << 20 // 1 MB

	if trustedProxy != "" {
		_, ipNet, err := net.ParseCIDR(trustedProxy)
		if err != nil {
			return nil, fmt.Errorf("invalid --trusted-proxy CIDR %q: %w", trustedProxy, err)
		}
		cfg.TrustedProxy = ipNet
	}

	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return strings.EqualFold(v, "true") || v == "1"
}
