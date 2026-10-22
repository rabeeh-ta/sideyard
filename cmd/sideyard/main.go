// Command sideyard runs the platform: the embedded proxy, the dashboard and
// API, and every project's app process.
//
//	sideyard                           start the platform
//	sideyard status                    what's running, and where Sideyard and its apps keep files
//	sideyard version                   print the version
//	sideyard help                      list the commands
//	sideyard set-password              set the admin password, read from stdin (used by the installer)
//	sideyard reset-password            print a new admin password and sign out all sessions
//	sideyard sqlite-snapshot SRC DST   copy a live SQLite database consistently (used internally)
//	sideyard sqlite-query DB           run a read-only query from stdin (used internally)
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"sideyard/internal/api"
	"sideyard/internal/config"
	"sideyard/internal/platform"
	"sideyard/internal/proxy"
	"sideyard/internal/runner"
	"sideyard/internal/secrets"
	"sideyard/internal/store"
	"sideyard/internal/update"
	"sideyard/internal/version"
	"sideyard/web"
)

func main() {
	// Caddy points the std logger at its JSON log on import; startup errors
	// read better plain. Caddy takes it over again once it loads its config.
	log.SetOutput(os.Stderr)
	log.SetFlags(0)
	// Helpers the platform runs as a project's user (Database tab, backups).
	if handled, err := store.RunHelper(os.Args[1:], os.Stdin, os.Stdout); handled {
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "":
		// Typed in a shell, which lacks the service's settings: say what
		// there is to type instead.
		if _, cerr := config.Load(); errors.Is(cerr, config.ErrNoDomain) && term.IsTerminal(int(os.Stdin.Fd())) {
			usage(os.Stderr)
			os.Exit(2)
		}
		// A new version that keeps failing to start is swapped back out.
		if rolledBack, rerr := update.BootAttempt(exe, version.Version); rerr != nil {
			log.Printf("sideyard: %v", rerr)
		} else if rolledBack {
			log.Printf("sideyard: %s failed to start several times; going back to the previous version", version.Version)
			err = reexec()
			break
		}
		var restart bool
		if restart, err = serve(); err == nil && restart {
			err = reexec()
		}
	case "status":
		err = status()
	case "version", "--version", "-v":
		fmt.Println(version.Version)
	case "help", "--help", "-h":
		usage(os.Stdout)
	case "set-password":
		err = setPassword()
	case "reset-password":
		err = resetPassword()
	default:
		err = fmt.Errorf("unknown command %q; sideyard help lists them", cmd)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `Sideyard %s runs Go + React + SQLite apps on this server.

Commands (status and reset-password need sudo):
  sideyard status           what's running, and where Sideyard and each app keep their files
  sideyard reset-password   print a new admin password and sign out every browser
  sideyard version          print the version
  sideyard help             show this

Sideyard itself runs as the sideyard service:
  sudo systemctl restart sideyard   start or restart it; apps keep running
  journalctl -u sideyard -f         follow its log
`, version.Version)
	if version.Source != "" {
		fmt.Fprintf(w, `
New versions install from the dashboard. To remove Sideyard and its apps:
  curl -fsSL %s/releases/latest/download/install.sh | sudo bash -s -- --uninstall
`, version.Source)
	}
}

// exe is this binary's path, read before an update can replace the file.
var exe, _ = os.Executable()

// reexec replaces this process with the binary now at exe: a newly
// installed update, or the previous one after a rollback. The PID stays the
// same, so systemd sees no restart.
func reexec() error {
	log.Printf("sideyard: restarting into the binary at %s", exe)
	return syscall.Exec(exe, os.Args, os.Environ())
}

func openStore(dataDir string) (*store.Store, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(dataDir, "sideyard.db"))
}

// serve runs the platform until it's told to stop, or until an update has
// been installed (restart is then set).
func serve() (restart bool, err error) {
	cfg, err := config.Load()
	if err != nil {
		return false, err
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return false, err
	}
	defer st.Close()
	box, err := secrets.LoadOrCreate(filepath.Join(cfg.DataDir, "master.key"))
	if err != nil {
		return false, err
	}
	if password, err := api.EnsurePassword(st); err != nil {
		return false, err
	} else if password != "" {
		// Plain stderr: once Caddy loads, the std logger writes JSON.
		fmt.Fprintf(os.Stderr, "\n  Admin password (shown once): %s\n  Change it any time with: sideyard reset-password\n\n", password)
	}

	rn, err := runner.New(cfg.Runner)
	if err != nil {
		return false, err
	}
	log.Printf("sideyard %s: running apps with the %s runner", version.Version, rn.Name())
	px := proxy.New(cfg)
	p := platform.New(cfg, st, box, px, rn)
	srv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           api.New(p, st, web.FS()).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", cfg.APIAddr)
	if err != nil {
		return false, fmt.Errorf("api listener: %w", err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := p.Boot(ctx); err != nil {
		return false, err
	}
	update.Booted(exe)
	log.Printf("sideyard: dashboard at %s", siteURL(cfg, cfg.DashboardHost))

	select {
	case <-ctx.Done():
	case <-p.RestartRequested():
		restart = true
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("api: %v", err)
		}
	}
	log.Printf("sideyard: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)
	px.Stop()
	p.Shutdown()
	// A stop (systemctl stop, a reboot) that came during an update's
	// restart wins: exit rather than start the new version.
	return restart && ctx.Err() == nil, nil
}

// siteURL is the address of a host Sideyard serves.
func siteURL(cfg config.Config, host string) string {
	if !cfg.Dev {
		return "https://" + host
	}
	url := "http://" + host
	if _, port, err := net.SplitHostPort(cfg.PublicAddr); err == nil && port != "80" {
		url += ":" + port
	}
	return url
}

// setPassword reads a new admin password from the first line of stdin, so
// it never appears in a process list or shell history.
func setPassword() error {
	dir, err := config.DataDir()
	if err != nil {
		return err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	password := strings.TrimRight(line, "\r\n")
	if err := api.CheckNewPassword(password); err != nil {
		return err
	}
	st, err := openStore(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := api.SetPassword(st, password); err != nil {
		return err
	}
	fmt.Println("Admin password set.")
	return nil
}

func resetPassword() error {
	dir, err := config.DataDir()
	if err != nil {
		return err
	}
	st, err := openStore(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	password := secrets.NewToken("")[:20]
	if err := api.SetPassword(st, password); err != nil {
		return err
	}
	fmt.Printf("New admin password: %s\n", password)
	return nil
}
