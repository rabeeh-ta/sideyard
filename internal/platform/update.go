package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"sideyard/internal/store"
	"sideyard/internal/update"
)

// Updates: Sideyard checks its release source every six hours and shows a
// newer version in the dashboard. Installing one swaps the binary, waits for
// deploys in progress, and asks main to restart into it. With the systemd
// runner apps keep running through the restart; only the proxy pauses.

const (
	updateFirstCheck    = time.Minute
	updateCheckInterval = 6 * time.Hour
)

type updateState struct {
	checking   bool
	installing bool
	restarting bool
	latest     *update.Manifest
	checkedAt  time.Time
	err        string
}

// LastUpdate records the most recent update installed from the dashboard.
type LastUpdate struct {
	From string    `json:"from"`
	To   string    `json:"to"`
	At   time.Time `json:"at"`
}

type UpdateStatus struct {
	Current string `json:"current"`
	// Source is where releases are looked for; "" when this build doesn't
	// check (local builds).
	Source    string    `json:"source"`
	Platform  string    `json:"platform"`
	Latest    string    `json:"latest,omitempty"`
	Published time.Time `json:"published,omitzero"`
	// Available is set when Latest is newer than Current, or Current is a
	// local build and Latest is any release.
	Available   bool        `json:"available"`
	CheckedAt   time.Time   `json:"checkedAt,omitzero"`
	Error       string      `json:"error,omitempty"`
	State       string      `json:"state"` // idle, checking, installing or restarting
	AutoInstall bool        `json:"autoInstall"`
	Last        *LastUpdate `json:"last,omitempty"`
	// RolledBack is a version that was installed, didn't start, and was
	// replaced by the previous binary. Automatic updates skip it.
	RolledBack *update.Rollback `json:"rolledBack,omitempty"`
}

const (
	settingAutoUpdate = "update_auto_install"
	settingLastUpdate = "update_last"
)

var updateClient = &http.Client{Timeout: 10 * time.Minute}

func (p *Platform) UpdateStatus() UpdateStatus {
	st := UpdateStatus{Current: p.version, Source: p.cfg.UpdateSource, Platform: update.Platform(), State: "idle"}
	st.AutoInstall = p.autoUpdate()
	if exe, err := p.exePath(); err == nil {
		if r, ok := update.RolledBack(exe); ok && r.From == p.version {
			st.RolledBack = &r
		}
	}
	if raw, err := p.store.Setting(settingLastUpdate); err == nil {
		var last LastUpdate
		if json.Unmarshal([]byte(raw), &last) == nil && last.To == p.version {
			st.Last = &last
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.upd
	switch {
	case u.restarting:
		st.State = "restarting"
	case u.installing:
		st.State = "installing"
	case u.checking:
		st.State = "checking"
	}
	st.CheckedAt, st.Error = u.checkedAt, u.err
	if u.latest != nil {
		st.Latest, st.Published = u.latest.Version, u.latest.Published
		st.Available = p.newer(u.latest.Version)
	}
	return st
}

// newer reports whether a release should replace this build.
func (p *Platform) newer(v string) bool {
	if p.version == "dev" {
		return true
	}
	return update.Newer(v, p.version)
}

func (p *Platform) autoUpdate() bool {
	v, _ := p.store.Setting(settingAutoUpdate)
	return v == "1"
}

func (p *Platform) SetAutoUpdate(on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return p.store.SetSetting(settingAutoUpdate, v)
}

// CheckForUpdate asks the release source for its newest version.
func (p *Platform) CheckForUpdate(ctx context.Context) UpdateStatus {
	if p.cfg.UpdateSource == "" {
		return p.UpdateStatus()
	}
	p.mu.Lock()
	if p.upd.checking || p.upd.installing || p.upd.restarting {
		p.mu.Unlock()
		return p.UpdateStatus()
	}
	p.upd.checking = true
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	m, err := update.Latest(ctx, updateClient, p.cfg.UpdateSource)
	p.mu.Lock()
	p.upd.checking = false
	p.upd.checkedAt = time.Now().UTC()
	if err != nil {
		p.upd.err = "couldn't check for updates: " + err.Error()
	} else {
		p.upd.err = ""
		p.upd.latest = &m
	}
	p.mu.Unlock()
	return p.UpdateStatus()
}

// InstallUpdate installs the newest release found by the last check, in the
// background. Watch UpdateStatus for progress; on success Sideyard restarts.
func (p *Platform) InstallUpdate() error {
	if p.cfg.UpdateSource == "" {
		return invalid("this build has no release source to update from")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.upd.installing || p.upd.restarting:
		return fmt.Errorf("%w: an update is already being installed", ErrConflict)
	case p.upd.latest == nil || !p.newer(p.upd.latest.Version):
		return invalid("there's no newer version to install")
	}
	p.upd.installing = true
	p.upd.err = ""
	m := *p.upd.latest
	p.bgWG.Go(func() { p.installUpdate(m) })
	return nil
}

func (p *Platform) installUpdate(m update.Manifest) {
	log.Printf("sideyard: installing %s", m.Version)
	err := p.swapBinary(m)
	p.mu.Lock()
	p.upd.installing = false
	if err != nil {
		p.upd.err = fmt.Sprintf("couldn't install %s: %v", m.Version, err)
		p.mu.Unlock()
		log.Printf("sideyard: update to %s failed: %v", m.Version, err)
		return
	}
	p.upd.restarting = true
	p.mu.Unlock()

	if exe, err := p.exePath(); err == nil {
		if err := update.MarkPending(exe, p.version, m.Version); err != nil {
			log.Printf("sideyard: mark %s pending: %v", m.Version, err)
		}
	}

	last, _ := json.Marshal(LastUpdate{From: p.version, To: m.Version, At: time.Now().UTC()})
	if err := p.store.SetSetting(settingLastUpdate, string(last)); err != nil {
		log.Printf("sideyard: record update: %v", err)
	}
	// Wait for deploys, restarts and imports in progress, and hold every
	// project's lock so no new one starts before the restart.
	projects, _ := p.store.Projects()
	for _, proj := range projects {
		p.lock(proj.ID)
	}
	log.Printf("sideyard: installed %s; restarting into it", m.Version)
	select {
	case p.restart <- struct{}{}:
	default:
	}
}

// exePath is the binary updates replace.
func (p *Platform) exePath() (string, error) {
	if p.exe != "" {
		return p.exe, nil
	}
	return os.Executable()
}

func (p *Platform) swapBinary(m update.Manifest) error {
	exe, err := p.exePath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(p.bg)
	defer cancel()
	return update.Install(ctx, updateClient, p.cfg.UpdateSource, m, exe)
}

// RestartRequested is signalled when Sideyard should restart into a newly
// installed binary.
func (p *Platform) RestartRequested() <-chan struct{} { return p.restart }

// updateLoop checks for a new release a minute after boot and every six
// hours, and installs it if automatic updates are on.
func (p *Platform) updateLoop() {
	if p.cfg.UpdateSource == "" {
		return
	}
	wait := updateFirstCheck
	for {
		select {
		case <-p.bg.Done():
			return
		case <-time.After(wait):
		}
		wait = updateCheckInterval
		st := p.CheckForUpdate(p.bg)
		failedBefore := st.RolledBack != nil && st.RolledBack.To == st.Latest
		if st.Available && st.AutoInstall && p.version != "dev" && !failedBefore {
			if err := p.InstallUpdate(); err != nil && !errors.Is(err, store.ErrConflict) {
				log.Printf("sideyard: automatic update: %v", err)
			}
		}
	}
}
