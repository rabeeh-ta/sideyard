// Package proxy runs an embedded Caddy that routes each domain to its
// project's current process and obtains HTTPS certificates automatically.
package proxy

import (
	"encoding/json"
	"path/filepath"
	"sync"

	"github.com/caddyserver/caddy/v2"
	// Only the modules Sideyard's config uses, not modules/standard.
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp"
	_ "github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"

	"sideyard/internal/config"
)

// Route sends requests for Hosts to Upstream (host:port). With no Upstream,
// the proxy answers 503 with Message instead. Paths matching Internal
// (e.g. "/_jobs/*") get a 404 from outside; only Sideyard calls them,
// directly on the app's port.
type Route struct {
	Hosts    []string
	Upstream string
	Message  string
	Internal []string
}

type Proxy struct {
	mu  sync.Mutex
	cfg config.Config
}

func New(cfg config.Config) *Proxy {
	return &Proxy{cfg: cfg}
}

// Apply replaces the whole routing table. Caddy swaps configs gracefully, so
// in-flight requests finish on the old config.
func (p *Proxy) Apply(routes []Route) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfgJSON, err := json.Marshal(p.build(routes))
	if err != nil {
		return err
	}
	return caddy.Load(cfgJSON, false)
}

func (p *Proxy) Stop() error {
	return caddy.Stop()
}

type obj = map[string]any

func (p *Proxy) build(routes []Route) obj {
	var caddyRoutes []obj
	for _, r := range routes {
		if len(r.Internal) > 0 {
			caddyRoutes = append(caddyRoutes, obj{
				"match":    []obj{{"host": r.Hosts, "path": r.Internal}},
				"handle":   []obj{textResponse(404, "Not found.")},
				"terminal": true,
			})
		}
		var handler obj
		if r.Upstream != "" {
			handler = obj{
				"handler":   "reverse_proxy",
				"upstreams": []obj{{"dial": r.Upstream}},
				// Every routing change reloads the whole config, and Caddy
				// closes WebSockets on reload by default. Keep them open;
				// they still close when their app's process is replaced.
				"stream_close_delay": "24h",
			}
		} else {
			handler = textResponse(503, r.Message)
		}
		caddyRoutes = append(caddyRoutes, obj{
			"match":    []obj{{"host": r.Hosts}},
			"handle":   []obj{handler},
			"terminal": true,
		})
	}
	caddyRoutes = append(caddyRoutes, obj{
		"handle": []obj{textResponse(404, "No app is configured for this domain.")},
	})

	server := obj{"routes": caddyRoutes}
	// Without a grace period, a reload or shutdown waits forever on
	// long-lived connections such as WebSockets.
	apps := obj{"http": obj{"grace_period": "10s", "servers": obj{"sideyard": server}}}
	if p.cfg.Dev {
		server["listen"] = []string{p.cfg.PublicAddr}
		server["automatic_https"] = obj{"disable": true}
	} else {
		// Caddy adds the :80 → HTTPS redirect and ACME challenges itself.
		server["listen"] = []string{":443"}
		if p.cfg.ACMEEmail != "" {
			apps["tls"] = obj{"automation": obj{"policies": []obj{{
				"issuers": []obj{{"module": "acme", "email": p.cfg.ACMEEmail}},
			}}}}
		}
	}
	return obj{
		"admin":   obj{"disabled": true, "config": obj{"persist": false}},
		"storage": obj{"module": "file_system", "root": filepath.Join(p.cfg.DataDir, "caddy")},
		"apps":    apps,
	}
}

func textResponse(status int, body string) obj {
	return obj{
		"handler":     "static_response",
		"status_code": status,
		"body":        body + "\n",
		"headers":     obj{"Content-Type": []string{"text/plain; charset=utf-8"}},
	}
}
