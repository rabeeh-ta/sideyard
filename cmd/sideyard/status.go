package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"sideyard/internal/config"
	"sideyard/internal/platform"
	"sideyard/internal/runner"
	"sideyard/internal/store"
	"sideyard/internal/version"
)

// status prints what's running, and every file Sideyard and its apps keep.
// It reads the sideyard service's settings first, so the paths and
// addresses are the ones the service uses.
func status() error {
	svc, _ := runner.ShowUnit("sideyard.service", "LoadState", "FragmentPath", "DropInPaths", "EnvironmentFiles", "Environment")
	installed := svc["LoadState"] == "loaded"
	if installed && os.Geteuid() != 0 {
		return errors.New("only root can read Sideyard's settings and database: sudo sideyard status")
	}
	envFile, _, _ := strings.Cut(svc["EnvironmentFiles"], " (")
	if installed {
		if err := loadServiceEnv(svc["Environment"], envFile); err != nil {
			return err
		}
	}
	cfg, err := config.Load()
	if err != nil {
		if !installed {
			return errors.New("there's no sideyard service on this machine")
		}
		return err
	}
	dbPath := filepath.Join(cfg.DataDir, "sideyard.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no Sideyard database at %s", dbPath)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	projects, err := st.Projects()
	if err != nil {
		return err
	}
	domains, err := st.Domains()
	if err != nil {
		return err
	}
	instance, _ := st.Setting("instance_id")

	useColors(os.Stdout)
	out := os.Stdout

	// Sideyard itself, in a line.
	self, state := runner.Status{}, "not installed as a service"
	if installed {
		self = runner.UnitStatus("sideyard.service")
		state = string(self.State)
	}
	facts := []string{paint(stateColor(state), state)}
	switch {
	case !installed:
	case self.State == runner.Running:
		facts = append(facts, "up "+uptime(self.StartedAt), fmt.Sprintf("pid %d", self.PID), size(int64(self.MemoryBytes))+" memory")
	case self.State == runner.Crashed:
		facts = append(facts, "see why: journalctl -u sideyard -n 50")
	default:
		facts = append(facts, "start it: sudo systemctl start sideyard")
	}
	fmt.Fprintf(out, "%s %s  %s\n", paint(stateColor(state), "●"), paint(bold, "Sideyard "+version.Version), strings.Join(facts, paint(dim, " · ")))
	fmt.Fprintf(out, "  %s\n", paint(blue, siteURL(cfg, cfg.DashboardHost)))
	disk := "disk: " + size(diskUsage(cfg.DataDir)) + " in " + cfg.DataDir
	if free, ok := freeSpace(cfg.DataDir); ok {
		disk += ", " + size(free) + " free"
	}
	fmt.Fprintf(out, "  %s\n", paint(dim, disk))

	// Every app, in a row.
	fmt.Fprintf(out, "\n%s\n", paint(bold, "APPS"))
	apps := [][]cell{{{text: "APP", color: dim}, {text: "STATE", color: dim}, {text: "RELEASE", color: dim}, {text: "UP", color: dim},
		{text: "MEMORY", color: dim, right: true}, {text: "DISK", color: dim, right: true}, {text: "ADDRESS", color: dim}}}
	if len(projects) == 0 {
		apps = [][]cell{{{text: "  " + paint(dim, "none yet: create one in the dashboard")}}}
	}
	states := map[int64]string{}
	for _, proj := range projects {
		state, s := appState(proj)
		states[proj.ID] = state
		release, up, memory := "-", "-", "-"
		if r, err := st.Release(proj.ActiveReleaseID); err == nil {
			release = fmt.Sprintf("#%d", r.Number)
		}
		if s.State == runner.Running && !s.StartedAt.IsZero() {
			up = uptime(s.StartedAt)
		}
		if s.MemoryBytes > 0 {
			memory = size(int64(s.MemoryBytes))
		}
		var urls []string
		for _, host := range domains[proj.ID] {
			urls = append(urls, siteURL(cfg, host))
		}
		apps = append(apps, []cell{{text: proj.Name, color: bold, dot: stateColor(state)}, {text: state, color: stateColor(state)},
			{text: release}, {text: up}, {text: memory, right: true},
			{text: size(diskUsage(filepath.Join(cfg.DataDir, "projects", proj.Name))), right: true}, {text: strings.Join(urls, ", "), color: blue}})
	}
	table(out, "  ", apps)

	// Where everything is: Sideyard's files, then each app's.
	files := [][]cell{{{text: paint(bold, "SIDEYARD")}}}
	item := func(label, path, size string) {
		files = append(files, []cell{{text: label, color: dim}, {text: path}, {text: size, color: blue, right: true}})
	}
	bin, err := filepath.EvalSymlinks(exe)
	if err != nil {
		bin = exe
	}
	item("program", bin, size(diskUsage(bin)))
	if exists(bin + ".prev") {
		item("previous", bin+".prev", size(diskUsage(bin+".prev")))
	}
	if link, err := exec.LookPath("sideyard"); err == nil && link != bin {
		if target, _ := filepath.EvalSymlinks(link); target == bin {
			item("link", link, "")
		}
	}
	if installed {
		item("service", svc["FragmentPath"], "")
		for _, d := range strings.Fields(svc["DropInPaths"]) {
			item("drop-in", d, "")
		}
		if envFile != "" {
			item("settings", envFile, "")
		}
	}
	item("data", cfg.DataDir+"/", size(diskUsage(cfg.DataDir)))
	listDir(item, cfg.DataDir)
	if installed {
		item("log", "journalctl -u sideyard -f", "")
	}
	for _, proj := range projects {
		dir := filepath.Join(cfg.DataDir, "projects", proj.Name)
		files = append(files, nil, []cell{{text: paint(stateColor(states[proj.ID]), "●") + " " + paint(bold, proj.Name)}})
		item("folder", dir+"/", size(diskUsage(dir)))
		listDir(item, dir)
		if strings.HasSuffix(proj.LiveInstance, ".service") {
			item("unit", proj.LiveInstance, "")
		}
		ref := runner.ProjectRef{ID: proj.ID, Name: proj.Name, Dir: dir}
		if name := runner.UnixUser(instance, ref); instance != "" && userExists(name) {
			item("user", name, "")
			item("log", fmt.Sprintf("journalctl SIDEYARD_PROJECT_ID=%d -f", proj.ID), "")
		}
	}
	fmt.Fprintln(out)
	table(out, "  ", files)
	return nil
}

// loadServiceEnv sets the variables the service gets, from its unit and
// then its environment file, which systemd lets override the unit.
func loadServiceEnv(unitEnv, envFile string) error {
	vars := strings.Fields(unitEnv)
	if envFile != "" {
		b, err := os.ReadFile(envFile)
		if err != nil {
			return err
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				vars = append(vars, line)
			}
		}
	}
	for _, kv := range vars {
		if k, v, ok := strings.Cut(kv, "="); ok {
			os.Setenv(strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return nil
}

// appState is what an app is doing, with its unit's numbers when it runs
// as one.
func appState(p store.Project) (string, runner.Status) {
	switch {
	case strings.HasSuffix(p.LiveInstance, ".service"):
		s := runner.UnitStatus(p.LiveInstance)
		return string(s.State), s
	case p.ActiveReleaseID == 0:
		return "not deployed", runner.Status{}
	case p.DesiredState == store.StateStopped:
		return "stopped", runner.Status{}
	case p.LiveInstance != "":
		return "running", runner.Status{} // inside Sideyard, under the process runner
	}
	return "not running", runner.Status{}
}

// listDir adds what's in a folder, one level down.
func listDir(item func(label, path, size string), dir string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		item("", "  "+name, size(diskUsage(filepath.Join(dir, e.Name()))))
	}
}

// --- output ---------------------------------------------------------------

// The colours are the logo's, as the installer prints them; none when the
// output isn't a terminal or NO_COLOR is set.
var bold, dim, green, red, blue, reset string

func useColors(f *os.File) {
	if !term.IsTerminal(int(f.Fd())) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return
	}
	bold, dim, reset = "\x1b[1m", "\x1b[2m", "\x1b[0m"
	green, red, blue = "\x1b[32m", "\x1b[31m", "\x1b[34m"
	if strings.Contains(os.Getenv("TERM"), "256") || os.Getenv("COLORTERM") != "" {
		green, red, blue = "\x1b[38;5;36m", "\x1b[38;5;203m", "\x1b[38;5;33m"
	}
}

func paint(color, s string) string {
	if color == "" || s == "" {
		return s
	}
	return color + s + reset
}

// stateColor: green for running, red for down, grey for nothing to run.
func stateColor(state string) string {
	switch state {
	case string(runner.Running):
		return green
	case "not deployed", "not installed as a service":
		return dim
	}
	return red
}

type cell struct {
	text  string
	color string
	right bool   // aligned right, as numbers are
	dot   string // the colour of a ● before the text
}

// table prints rows indented, with each column as wide as its widest cell.
// A row of one cell is a heading, printed as it is, unindented and left out
// of the widths; a nil row is a blank line.
func table(w io.Writer, indent string, rows [][]cell) {
	var widths []int
	dots := map[int]bool{} // columns with a ● somewhere, which every row makes room for
	for _, r := range rows {
		if len(r) < 2 {
			continue
		}
		for i, c := range r {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(c.text))
			dots[i] = dots[i] || c.dot != ""
		}
	}
	for _, r := range rows {
		if len(r) < 2 {
			if len(r) == 1 {
				fmt.Fprint(w, r[0].text)
			}
			fmt.Fprintln(w)
			continue
		}
		for len(r) > 0 && r[len(r)-1].text == "" {
			r = r[:len(r)-1]
		}
		var b strings.Builder
		b.WriteString(indent)
		for i, c := range r {
			if dots[i] {
				if c.dot != "" {
					b.WriteString(paint(c.dot, "●") + " ")
				} else {
					b.WriteString("  ")
				}
			}
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c.text))
			switch {
			case c.right:
				b.WriteString(pad + paint(c.color, c.text))
			case i < len(r)-1:
				b.WriteString(paint(c.color, c.text) + pad)
			default:
				b.WriteString(paint(c.color, c.text))
			}
			if i < len(r)-1 {
				b.WriteString("   ")
			}
		}
		fmt.Fprintln(w, b.String())
	}
}

func size(n int64) string { return platform.HumanBytes(n) }

func uptime(since time.Time) string {
	d := time.Since(since)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// diskUsage is the size of a file, or of everything in a folder.
func diskUsage(path string) int64 {
	var n int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func freeSpace(dir string) (int64, bool) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, false
	}
	return int64(uint64(s.Bavail) * uint64(s.Bsize)), true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}
