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
	DBDriver          string // "sqlite" (default) or "mysql"
	DBPath            string // SQLite file path (ignored when DBDriver=mysql)
	DBHost            string // MySQL/MariaDB host (used when DBDriver=mysql)
	DBName            string // MySQL/MariaDB database name (used when DBDriver=mysql)
	DBUser            string // MySQL/MariaDB username (used when DBDriver=mysql)
	DBPassword        string // MySQL/MariaDB password (used when DBDriver=mysql)
	NotesRoot         string
	AllowRegistration bool
	SecureCookies     bool       // set true when serving over HTTPS
	TrustedProxy      *net.IPNet // nil means trust nothing (direct connections only)
	MaxContentBytes    int64
	WatchInterval      time.Duration // 0 disables the disk watcher
	SkipReconciliation bool          // skip startup hash-check scan
	EnableGitHistory   bool          // record every note save/delete as a git commit
	SeedDev            bool          // seed a dev user + folder tree + ~100 notes on first boot
}

func Parse() (*Config, error) {
	cfg := &Config{}
	var trustedProxy string

	flag.StringVar(&cfg.Addr, "addr", envOr("THORNOTES_ADDR", ":8080"), "listen address")
	flag.StringVar(&cfg.DBDriver, "db-driver", envOr("THORNOTES_DB_DRIVER", "sqlite"), "database driver: sqlite or mysql")
	flag.StringVar(&cfg.DBPath, "db", envOr("THORNOTES_DB", "thornotes.db"), "SQLite database path (sqlite driver only)")
	flag.StringVar(&cfg.DBHost, "db-host", envOr("THORNOTES_DB_HOST", "localhost:3306"), "MySQL/MariaDB host:port (mysql driver only)")
	flag.StringVar(&cfg.DBName, "db-name", envOr("THORNOTES_DB_NAME", "thornotes"), "MySQL/MariaDB database name (mysql driver only)")
	flag.StringVar(&cfg.DBUser, "db-user", envOr("THORNOTES_DB_USER", ""), "MySQL/MariaDB username (mysql driver only)")
	flag.StringVar(&cfg.DBPassword, "db-password", envOr("THORNOTES_DB_PASSWORD", ""), "MySQL/MariaDB password (mysql driver only)")
	flag.StringVar(&cfg.NotesRoot, "notes-root", envOr("THORNOTES_NOTES_ROOT", "notes"), "root directory for note files")
	flag.BoolVar(&cfg.AllowRegistration, "allow-registration", envBool("THORNOTES_ALLOW_REGISTRATION", true), "allow new user registration")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", envBool("THORNOTES_SECURE_COOKIES", false), "set Secure flag on session cookie (enable when serving over HTTPS)")
	flag.StringVar(&trustedProxy, "trusted-proxy", envOr("THORNOTES_TRUSTED_PROXY", ""), "CIDR of trusted reverse proxy (e.g. 10.0.0.0/8)")
	flag.DurationVar(&cfg.WatchInterval, "watch-interval", envDuration("THORNOTES_WATCH_INTERVAL", 30*time.Second), "disk watch poll interval (0 to disable)")
	flag.BoolVar(&cfg.SkipReconciliation, "skip-reconciliation", envBool("THORNOTES_SKIP_RECONCILIATION", false), "skip startup reconciliation scan (use on trusted restarts with large note corpora)")
	flag.BoolVar(&cfg.EnableGitHistory, "enable-git-history", envBool("THORNOTES_ENABLE_GIT_HISTORY", false), "record every note save/delete as a git commit in the notes directory")
	flag.BoolVar(&cfg.SeedDev, "seed-dev", envBool("THORNOTES_SEED_DEV", false), "development-only: create a 'dev' user with a realistic folder tree and ~100 seeded notes if the user does not already exist")
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
