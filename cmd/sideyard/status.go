package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"sideyard/internal/config"
	"sideyard/internal/runner"
	"sideyard/internal/store"
	"sideyard/internal/version"
)

// status prints what's running, and every file Sideyard and its apps keep.
// It reads the sideyard service's settings first, so the paths and
// addresses are the ones the service uses.
func status() error {
	svc, _ := runner.ShowUnit("sideyard.service", "LoadState", "ActiveState", "MainPID",
		"ActiveEnterTimestamp", "FragmentPath", "DropInPaths", "EnvironmentFiles", "Environment")
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

	fmt.Printf("Sideyard %s: %s\n", version.Version, serviceState(installed, svc))
	fmt.Printf("Dashboard: %s\n", siteURL(cfg, cfg.DashboardHost))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	defer w.Flush()
	row := func(what, about string) { fmt.Fprintf(w, "%s\t%s\n", what, about) }

	fmt.Fprintln(w, "\nSideyard")
	bin, err := filepath.EvalSymlinks(exe)
	if err != nil {
		bin = exe
	}
	row("  "+bin, "the program")
	if exists(bin + ".prev") {
		row("  "+bin+".prev", "the version before the last update")
	}
	if link, err := exec.LookPath("sideyard"); err == nil && link != bin {
		if target, _ := filepath.EvalSymlinks(link); target == bin {
			row("  "+link, "a link to it, so your shell finds it")
		}
	}
	if installed {
		row("  "+svc["FragmentPath"], "the service that runs it")
		for _, d := range strings.Fields(svc["DropInPaths"]) {
			row("  "+d, "a change to the service")
		}
		if envFile != "" {
			row("  "+envFile, "its settings")
		}
	}
	row("  "+cfg.DataDir+"/", "its data, "+megabytes(dirSize(cfg.DataDir))+" in all")
	listDir(row, cfg.DataDir, map[string]string{
		"sideyard.db": "projects, releases, settings and encrypted variables",
		"master.key":  "the key to the encrypted variables; keep a copy somewhere safe",
		"caddy":       "TLS certificates",
		"projects":    "a folder for each app, below",
	})
	if installed {
		row("  journalctl -u sideyard -f", "its log")
	}

	if len(projects) == 0 {
		fmt.Fprintln(w, "\nNo apps yet.")
	}
	for _, proj := range projects {
		ref := runner.ProjectRef{ID: proj.ID, Name: proj.Name, Dir: filepath.Join(cfg.DataDir, "projects", proj.Name)}
		head := proj.Name + ": " + appState(proj)
		for _, host := range domains[proj.ID] {
			head += ", " + siteURL(cfg, host)
		}
		fmt.Fprintln(w, "\n"+head)
		row("  "+ref.Dir+"/", megabytes(dirSize(ref.Dir))+" in all")
		listDir(row, ref.Dir, map[string]string{
			"data":     "its database and files",
			"releases": "its latest binaries, for rollback",
			"run":      "the running release's environment, variables included",
			"logs":     "its log",
		})
		if strings.HasSuffix(proj.LiveInstance, ".service") {
			row("  "+proj.LiveInstance, "the running release")
		}
		if name := runner.UnixUser(instance, ref); instance != "" && userExists(name) {
			row("  user "+name, "runs it, and owns data/")
			row(fmt.Sprintf("  journalctl SIDEYARD_PROJECT_ID=%d -f", proj.ID), "its log")
		}
	}
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

func serviceState(installed bool, svc map[string]string) string {
	if !installed {
		return "not installed as a service"
	}
	switch svc["ActiveState"] {
	case "active":
		return fmt.Sprintf("running since %s (pid %s)", svc["ActiveEnterTimestamp"], svc["MainPID"])
	case "activating", "reloading":
		return "starting"
	case "failed":
		return "failed; see why: journalctl -u sideyard -n 50"
	}
	return "stopped; start it: sudo systemctl start sideyard"
}

func appState(p store.Project) string {
	switch {
	case strings.HasSuffix(p.LiveInstance, ".service"):
		return string(runner.UnitStatus(p.LiveInstance).State)
	case p.ActiveReleaseID == 0:
		return "not deployed yet"
	case p.DesiredState == store.StateStopped:
		return "stopped"
	case p.LiveInstance != "":
		return "running, inside Sideyard"
	}
	return "not running"
}

// listDir prints a folder's entries one level down, describing the ones
// it knows. SQLite's -wal and -shm files go with their database.
func listDir(row func(string, string), dir string, about map[string]string) {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			continue
		}
		label := name
		if e.IsDir() {
			label += "/"
		}
		row("    "+label, about[name])
	}
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

func megabytes(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func userExists(name string) bool {
	_, err := user.Lookup(name)
	return err == nil
}
