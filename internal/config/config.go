package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

type Config struct {
	Addr              string
	DBPath            string
	NotesRoot         string
	AllowRegistration bool
	SecureCookies     bool       // set true when serving over HTTPS
	TrustedProxy      *net.IPNet // nil means trust nothing (direct connections only)
	MaxContentBytes   int64
	WatchInterval     time.Duration // 0 disables the disk watcher
}

func Parse() (*Config, error) {
	cfg := &Config{}
	var trustedProxy string

	flag.StringVar(&cfg.Addr, "addr", envOr("THORNOTES_ADDR", ":8080"), "listen address")
	flag.StringVar(&cfg.DBPath, "db", envOr("THORNOTES_DB", "thornotes.db"), "SQLite database path")
	flag.StringVar(&cfg.NotesRoot, "notes-root", envOr("THORNOTES_NOTES_ROOT", "notes"), "root directory for note files")
	flag.BoolVar(&cfg.AllowRegistration, "allow-registration", envBool("THORNOTES_ALLOW_REGISTRATION", true), "allow new user registration")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", envBool("THORNOTES_SECURE_COOKIES", false), "set Secure flag on session cookie (enable when serving over HTTPS)")
	flag.StringVar(&trustedProxy, "trusted-proxy", envOr("THORNOTES_TRUSTED_PROXY", ""), "CIDR of trusted reverse proxy (e.g. 10.0.0.0/8)")
	flag.DurationVar(&cfg.WatchInterval, "watch-interval", envDuration("THORNOTES_WATCH_INTERVAL", 30*time.Second), "disk watch poll interval (0 to disable)")
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

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
