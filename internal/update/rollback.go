package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// An update that doesn't boot is rolled back. Before Sideyard restarts into
// a new binary it marks it pending; each start of that binary counts an
// attempt, and a successful boot clears the mark. If systemd keeps
// restarting a binary that never boots, the next start puts the previous
// one back. Under the systemd unit (Restart=always, RestartSec=2) that
// takes a few seconds; without a supervisor, nothing restarts it.

// maxBootAttempts is how many starts a new binary gets.
const maxBootAttempts = 3

// Rollback describes an update that was undone.
type Rollback struct {
	From string    `json:"from"` // the version running again
	To   string    `json:"to"`   // the version that didn't boot
	At   time.Time `json:"at"`
}

type pending struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Attempts int    `json:"attempts"`
}

func pendingPath(exe string) string  { return exe + ".pending" }
func rollbackPath(exe string) string { return exe + ".rolledback" }

// MarkPending records that the binary at exe is version to, replacing
// version from, and hasn't booted yet.
func MarkPending(exe, from, to string) error {
	exe, err := resolve(exe)
	if err != nil {
		return err
	}
	os.Remove(rollbackPath(exe))
	return writeJSON(pendingPath(exe), pending{From: from, To: to})
}

// BootAttempt is called when Sideyard starts, as version current. It
// counts an attempt for a pending update; once the attempts are used up it
// puts the previous binary back at exe and reports true: the caller should
// exec exe again to run it.
func BootAttempt(exe, current string) (rolledBack bool, err error) {
	exe, err = resolve(exe)
	if err != nil {
		return false, err
	}
	var p pending
	if err := readJSON(pendingPath(exe), &p); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		os.Remove(pendingPath(exe))
		return false, err
	}
	if p.To != current { // replaced by hand since; not ours to judge
		return false, os.Remove(pendingPath(exe))
	}
	if p.Attempts < maxBootAttempts {
		p.Attempts++
		return false, writeJSON(pendingPath(exe), p)
	}
	prev := exe + ".prev"
	if _, err := os.Stat(prev); err != nil {
		os.Remove(pendingPath(exe))
		return false, fmt.Errorf("%s didn't boot in %d attempts, and there's no previous binary to go back to", current, maxBootAttempts)
	}
	os.Remove(exe + ".failed")
	if err := os.Rename(exe, exe+".failed"); err != nil {
		return false, err
	}
	if err := os.Rename(prev, exe); err != nil {
		os.Rename(exe+".failed", exe)
		return false, err
	}
	os.Remove(pendingPath(exe))
	writeJSON(rollbackPath(exe), Rollback{From: p.From, To: p.To, At: time.Now().UTC()})
	return true, nil
}

// Booted clears the pending mark: the binary at exe works.
func Booted(exe string) {
	if exe, err := resolve(exe); err == nil {
		os.Remove(pendingPath(exe))
	}
}

// RolledBack reports the last update undone at exe, if any.
func RolledBack(exe string) (Rollback, bool) {
	var r Rollback
	exe, err := resolve(exe)
	if err != nil {
		return r, false
	}
	return r, readJSON(rollbackPath(exe), &r) == nil
}

func resolve(exe string) (string, error) {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return real, nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
