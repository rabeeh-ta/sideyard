// Package config loads Sideyard's settings from SIDEYARD_* environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sideyard/internal/version"
)

type Config struct {
	// DataDir holds the platform database, the master key, TLS certificates and
	// every project's releases, data and logs.
	DataDir string
	// Domain is the base domain. Each project gets <name>.<Domain> automatically.
	Domain string
	// DashboardHost is where the Sideyard dashboard is served.
	DashboardHost string
	// ACMEEmail is passed to Let's Encrypt. Optional.
	ACMEEmail string
	// Dev serves plain HTTP on PublicAddr instead of HTTPS on :443.
	Dev bool
	// PublicAddr is the listen address in dev mode.
	PublicAddr string
	// APIAddr is the internal listener for the dashboard and API. The proxy
	// forwards DashboardHost to it.
	APIAddr string
	// Runner is "systemd", "process", or "" to pick systemd when available.
	Runner string
	// UpdateSource is where new releases are looked for: the build's own
	// source unless SIDEYARD_UPDATE_SOURCE overrides it, "" (or "off") for
	// no update checks.
	UpdateSource string
}

func Load() (Config, error) {
	c := Config{
		Domain:     strings.ToLower(strings.TrimSpace(os.Getenv("SIDEYARD_DOMAIN"))),
		ACMEEmail:  os.Getenv("SIDEYARD_ACME_EMAIL"),
		Dev:        os.Getenv("SIDEYARD_DEV") == "1",
		PublicAddr: envOr("SIDEYARD_PUBLIC_ADDR", ":8080"),
		APIAddr:    envOr("SIDEYARD_API_ADDR", "127.0.0.1:7070"),
		Runner:     os.Getenv("SIDEYARD_RUNNER"),
	}
	c.UpdateSource = strings.TrimRight(envOr("SIDEYARD_UPDATE_SOURCE", version.Source), "/")
	if c.UpdateSource == "off" {
		c.UpdateSource = ""
	}
	if c.UpdateSource != "" {
		if err := CheckUpdateSource(c.UpdateSource); err != nil {
			return c, err
		}
	}
	if c.ACMEEmail != "" && !emailPattern.MatchString(c.ACMEEmail) {
		return c, fmt.Errorf("SIDEYARD_ACME_EMAIL %q isn't an email address; Let's Encrypt would refuse it and no certificate would be issued", c.ACMEEmail)
	}
	if c.Dev && c.Domain == "" {
		c.Domain = "localhost"
	}
	if c.Domain == "" {
		return c, ErrNoDomain
	}
	c.DashboardHost = strings.ToLower(envOr("SIDEYARD_DASHBOARD_HOST", "sideyard."+c.Domain))

	dir, err := DataDir()
	if err != nil {
		return c, err
	}
	c.DataDir = dir
	return c, nil
}

// DataDir resolves SIDEYARD_DATA_DIR to an absolute path, defaulting to
// /var/lib/sideyard, or .sideyard-data in dev mode.
func DataDir() (string, error) {
	dir := os.Getenv("SIDEYARD_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/sideyard"
		if os.Getenv("SIDEYARD_DEV") == "1" {
			dir = ".sideyard-data"
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("data dir: %w", err)
	}
	return abs, nil
}

var ErrNoDomain = errors.New("SIDEYARD_DOMAIN is required, e.g. SIDEYARD_DOMAIN=example.com")

var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// CheckUpdateSource accepts https sources, and plain http only on this
// machine: a release's checksums come from the same server as its binary,
// so only TLS stops someone on the network path from substituting both.
func CheckUpdateSource(source string) error {
	u, err := url.Parse(source)
	if err != nil || u.Host == "" {
		return fmt.Errorf("update source %q isn't a URL", source)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && isLoopback(u.Hostname()):
		return nil
	}
	return fmt.Errorf("update source %q must use https (plain http is allowed only for localhost)", source)
}

func isLoopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
