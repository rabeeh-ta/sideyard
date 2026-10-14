package config

import "testing"

func TestCheckUpdateSource(t *testing.T) {
	for source, ok := range map[string]bool{
		"https://github.com/owner/repo": true,
		"http://127.0.0.1:8299":         true,
		"http://localhost:8765":         true,
		"http://[::1]:8765":             true,
		"http://releases.example.com":   false,
		"http://host.docker.internal":   false,
		"ftp://example.com":             false,
		"github.com/owner/repo":         false,
	} {
		if err := CheckUpdateSource(source); (err == nil) != ok {
			t.Errorf("%s: %v", source, err)
		}
	}
}

func TestLoadChecksSettings(t *testing.T) {
	t.Setenv("SIDEYARD_DOMAIN", "example.com")
	t.Setenv("SIDEYARD_DATA_DIR", t.TempDir())
	t.Setenv("SIDEYARD_UPDATE_SOURCE", "off")
	for email, ok := range map[string]bool{"": true, "you@example.com": true, "you": false, "you@example": false, "a b@example.com": false} {
		t.Setenv("SIDEYARD_ACME_EMAIL", email)
		if _, err := Load(); (err == nil) != ok {
			t.Errorf("email %q: %v", email, err)
		}
	}
	t.Setenv("SIDEYARD_ACME_EMAIL", "")
	t.Setenv("SIDEYARD_UPDATE_SOURCE", "http://example.com")
	if _, err := Load(); err == nil {
		t.Error("accepted a plain-http update source")
	}
}
