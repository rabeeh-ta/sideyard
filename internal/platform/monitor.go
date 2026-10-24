package platform

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Monitoring: CPU per app (from samples of its CPU time), a health check of
// every live app every 30 seconds, the size of each project's data, and
// whether each domain points here and has a certificate.

const (
	healthInterval  = 30 * time.Second
	sizeCacheMaxAge = time.Minute
)

type cpuSample struct {
	nanos   uint64
	at      time.Time
	percent float64
}

// cpuPercent turns a cumulative CPU time into a percentage of one core
// since the previous sample of the same instance (so 200 means two cores).
func (p *Platform) cpuPercent(instance string, nanos uint64) float64 {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	prev, ok := p.cpuSamples[instance]
	if ok && now.Sub(prev.at) < time.Second {
		return prev.percent // polled again too soon to measure
	}
	s := cpuSample{nanos: nanos, at: now}
	if ok && nanos >= prev.nanos {
		s.percent = 100 * float64(nanos-prev.nanos) / float64(now.Sub(prev.at).Nanoseconds())
	}
	p.cpuSamples[instance] = s
	return s.percent
}

type healthState struct {
	OK        bool
	CheckedAt time.Time
	Error     string
}

// Health is a live app's latest periodic health check.
type Health struct {
	Status    string    `json:"status"` // healthy, unhealthy or unknown
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Error     string    `json:"error,omitempty"`
}

func (p *Platform) health(projectID int64) Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.healthStates[projectID]
	switch {
	case !ok:
		return Health{Status: "unknown"}
	case h.OK:
		return Health{Status: "healthy", CheckedAt: h.CheckedAt}
	}
	return Health{Status: "unhealthy", CheckedAt: h.CheckedAt, Error: h.Error}
}

func (p *Platform) healthLoop() {
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	for {
		select {
		case <-p.bg.Done():
			return
		case <-t.C:
		}
		p.checkHealth()
	}
}

// checkHealth checks every live app and logs when one changes state.
func (p *Platform) checkHealth() {
	projects, err := p.store.Projects()
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, proj := range projects {
		l := p.getLive(proj.ID)
		if l == nil {
			p.mu.Lock()
			delete(p.healthStates, proj.ID)
			p.mu.Unlock()
			continue
		}
		st := healthState{OK: true, CheckedAt: time.Now().UTC()}
		resp, err := client.Get(healthURL(l.port, proj.HealthPath))
		switch {
		case err != nil:
			st.OK, st.Error = false, err.Error()
		case resp.StatusCode >= 400:
			st.OK, st.Error = false, fmt.Sprintf("%s answered %d", proj.HealthPath, resp.StatusCode)
		}
		if resp != nil {
			resp.Body.Close()
		}
		p.mu.Lock()
		prev, known := p.healthStates[proj.ID]
		p.healthStates[proj.ID] = st
		p.mu.Unlock()
		switch {
		case !st.OK && (!known || prev.OK):
			p.logf(proj, "health check failing: %s", st.Error)
		case st.OK && known && !prev.OK:
			p.logf(proj, "health check passing again")
		}
	}
}

type sizeEntry struct {
	bytes int64
	at    time.Time
}

// DataSize is the disk space a project's DATA_DIR uses, cached for a minute.
func (p *Platform) DataSize(name string) int64 {
	p.mu.Lock()
	e, ok := p.sizes[name]
	p.mu.Unlock()
	if ok && time.Since(e.at) < sizeCacheMaxAge {
		return e.bytes
	}
	var total int64
	filepath.WalkDir(p.dir(name, "data"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	// An empty dir is usually an app that hasn't written yet: measure again
	// next time instead of showing nothing for a minute.
	if total > 0 {
		p.mu.Lock()
		p.sizes[name] = sizeEntry{bytes: total, at: time.Now()}
		p.mu.Unlock()
	}
	return total
}

// DomainStatus says where a domain points and what certificate it has.
type DomainStatus struct {
	Host      string   `json:"host"`
	Addresses []string `json:"addresses"`
	// PointsHere is nil when Sideyard can't tell (no reference address).
	PointsHere *bool  `json:"pointsHere"`
	DNSError   string `json:"dnsError,omitempty"`
	// Certificate is nil until one has been issued (never in dev mode).
	Certificate *CertInfo `json:"certificate"`
}

type CertInfo struct {
	Issuer    string    `json:"issuer"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
}

func (p *Platform) DomainStatus(ctx context.Context, name string) ([]DomainStatus, error) {
	proj, err := p.store.ProjectByName(name)
	if err != nil {
		return nil, err
	}
	domains, err := p.store.Domains()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	here := p.serverAddresses(ctx)
	out := []DomainStatus{}
	for _, host := range domains[proj.ID] {
		ds := DomainStatus{Host: host, Addresses: []string{}}
		if p.cfg.Dev && (host == "localhost" || strings.HasSuffix(host, ".localhost")) {
			yes := true
			ds.Addresses, ds.PointsHere = []string{"127.0.0.1"}, &yes
		} else if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
			ds.DNSError = dnsError(err)
		} else {
			ds.Addresses = addrs
			if len(here) > 0 {
				match := slices.ContainsFunc(addrs, func(a string) bool { return slices.Contains(here, a) })
				ds.PointsHere = &match
			}
		}
		ds.Certificate = p.certificate(host)
		out = append(out, ds)
	}
	return out, nil
}

// PublicAddresses are where a domain's DNS records should point: those in
// SIDEYARD_PUBLIC_IP and the dashboard host's (it reaches this server by
// definition).
func (p *Platform) PublicAddresses(ctx context.Context) []string {
	var out []string
	for _, ip := range strings.Split(os.Getenv("SIDEYARD_PUBLIC_IP"), ",") {
		if ip = strings.TrimSpace(ip); ip != "" {
			out = append(out, ip)
		}
	}
	if !p.cfg.Dev {
		if addrs, err := net.DefaultResolver.LookupHost(ctx, p.cfg.DashboardHost); err == nil {
			out = append(out, addrs...)
		}
	}
	return out
}

// serverAddresses are the addresses this server is reached at: its public
// addresses and the machine's own interfaces.
func (p *Platform) serverAddresses(ctx context.Context) []string {
	out := p.PublicAddresses(ctx)
	if ifaces, err := net.InterfaceAddrs(); err == nil {
		for _, a := range ifaces {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

func dnsError(err error) string {
	if de, ok := err.(*net.DNSError); ok && de.IsNotFound {
		return "no DNS record"
	}
	return err.Error()
}

// issuerName names who signed cert: the organization ("Let's Encrypt"),
// or Caddy's own CA, which signs names a public CA won't (*.localhost).
func issuerName(cert *x509.Certificate) string {
	if strings.HasPrefix(cert.Issuer.CommonName, "Caddy Local Authority") {
		return "this server's own CA"
	}
	if len(cert.Issuer.Organization) > 0 {
		return cert.Issuer.Organization[0]
	}
	return cert.Issuer.CommonName
}

// certificate reads the certificate Caddy obtained for host, if any.
func (p *Platform) certificate(host string) *CertInfo {
	matches, _ := filepath.Glob(filepath.Join(p.cfg.DataDir, "caddy", "certificates", "*", host, host+".crt"))
	var best *CertInfo
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		block, _ := pem.Decode(b)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if best == nil || cert.NotAfter.After(best.NotAfter) {
			best = &CertInfo{Issuer: issuerName(cert), NotBefore: cert.NotBefore.UTC(), NotAfter: cert.NotAfter.UTC()}
		}
	}
	return best
}
