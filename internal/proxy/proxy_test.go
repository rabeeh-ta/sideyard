package proxy

import (
	"encoding/json"
	"testing"

	"github.com/caddyserver/caddy/v2"

	"sideyard/internal/config"
)

// The production config can't be exercised locally (it needs public DNS and
// ports 80/443), so at least make sure Caddy accepts it.
func TestConfigsValidate(t *testing.T) {
	routes := []Route{
		{Hosts: []string{"sideyard.example.com"}, Upstream: "127.0.0.1:7070"},
		{Hosts: []string{"notes.example.com", "notes.app"}, Upstream: "127.0.0.1:20001", Internal: []string{"/_jobs/*"}},
		{Hosts: []string{"idle.example.com"}, Message: "idle is stopped."},
	}
	for _, tc := range []struct {
		name string
		cfg  config.Config
	}{
		{"production", config.Config{DataDir: t.TempDir(), ACMEEmail: "ops@example.com"}},
		{"dev", config.Config{DataDir: t.TempDir(), Dev: true, PublicAddr: "127.0.0.1:0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(New(tc.cfg).build(routes))
			if err != nil {
				t.Fatal(err)
			}
			var cfg caddy.Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			if err := caddy.Validate(&cfg); err != nil {
				t.Fatalf("caddy rejected config: %v\n%s", err, raw)
			}
		})
	}
}
