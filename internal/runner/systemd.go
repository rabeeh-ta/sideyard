package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Systemd runs each release as a transient systemd service, as the
// project's own Unix user, sandboxed to write only its data dir, with
// memory and CPU limits. Units live in sideyard.slice and keep running when
// Sideyard restarts. Output goes to journald, tagged with the project ID.
//
// Secrets never appear in unit properties (which any local user can read
// over D-Bus): the environment is passed in a root-only EnvironmentFile.
type Systemd struct {
	dataDir  string
	instance string
	journal  journalWriter
}

func NewSystemd() (*Systemd, error) {
	if !SystemdAvailable() {
		return nil, errors.New("the systemd runner needs Linux with systemd, running as root")
	}
	return &Systemd{}, nil
}

func (r *Systemd) Name() string   { return "systemd" }
func (r *Systemd) Detached() bool { return true }

// Init lets project users traverse the data dir without listing it.
// Sideyard's own files in it (database, master key) stay root-only.
func (r *Systemd) Init(dataDir, instance string) error {
	r.dataDir, r.instance = dataDir, instance
	projects := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(projects, 0o711); err != nil {
		return err
	}
	for _, d := range []string{dataDir, projects} {
		if err := os.Chmod(d, 0o711); err != nil {
			return err
		}
	}
	return nil
}

func (r *Systemd) Prepare(p ProjectRef) error {
	name := r.user(p)
	if _, err := user.Lookup(name); err != nil {
		shell := "/usr/sbin/nologin"
		if _, err := os.Stat(shell); err != nil {
			shell = "/bin/false"
		}
		out, err := exec.Command("useradd", "--system", "--user-group", "--no-create-home",
			"--home-dir", p.DataDir(), "--shell", shell, name).CombinedOutput()
		if err != nil {
			return fmt.Errorf("useradd %s: %v: %s", name, err, strings.TrimSpace(string(out)))
		}
	}
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{p.Dir, 0o711}, {p.DataDir(), 0o750}, {p.ReleasesDir(), 0o750}, {filepath.Join(p.Dir, "run"), 0o700}} {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return err
		}
		if err := os.Chmod(d.path, d.mode); err != nil {
			return err
		}
	}
	// Hand the data over only if it isn't the project's yet (a new project,
	// or one that ran under the process runner): walking a big data dir
	// on every boot would be slow.
	uid, _, err := lookupIDs(name)
	if err != nil {
		return err
	}
	if st, err := os.Stat(p.DataDir()); err == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); !ok || int(sys.Uid) != uid {
			if err := r.Own(p, p.DataDir(), AppWritable); err != nil {
				return err
			}
		}
	}
	return r.Own(p, p.ReleasesDir(), AppReadable)
}

func (r *Systemd) Own(p ProjectRef, path string, o Ownership) error {
	uid, gid, err := lookupIDs(r.user(p))
	if err != nil {
		return err
	}
	return filepath.WalkDir(path, func(f string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch o {
		case AppWritable:
			return os.Lchown(f, uid, gid)
		default:
			if err := os.Lchown(f, 0, gid); err != nil {
				return err
			}
			return os.Chmod(f, 0o750)
		}
	})
}

func (r *Systemd) Credential(p ProjectRef) (*syscall.Credential, error) {
	uid, gid, err := lookupIDs(r.user(p))
	if err != nil {
		return nil, err
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, nil
}

func (r *Systemd) Start(s Spec) (Instance, error) {
	unit := fmt.Sprintf("%s%s-r%d-%s.service", r.unitPrefix(), s.Project.Name, s.Release, strconv.FormatInt(time.Now().UnixMilli(), 36))
	envPath := envFilePath(s.Project, unit)
	if err := os.WriteFile(envPath, encodeEnvFile(s.Env), 0o600); err != nil {
		return nil, err
	}
	out, err := exec.Command("systemd-run", runArgs(s, r.instance, unit, envPath, r.user(s.Project))...).CombinedOutput()
	if err != nil {
		os.Remove(envPath)
		return nil, fmt.Errorf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return &unitInstance{unit: unit, envPath: envPath}, nil
}

// runArgs is the systemd-run command line for a release. Its journal
// entries carry the instance as well as the project, because project IDs
// repeat across installations on one machine.
func runArgs(s Spec, instance, unit, envPath, userName string) []string {
	props := []string{
		"User=" + userName,
		"Group=" + userName,
		"EnvironmentFile=" + envPath,
		"Restart=on-failure",
		"RestartSec=3",
		"TimeoutStopSec=10",
		"SyslogIdentifier=" + s.Project.Name,
		"LogExtraFields=SIDEYARD_INSTANCE=" + instance,
		fmt.Sprintf("LogExtraFields=SIDEYARD_PROJECT_ID=%d", s.Project.ID),
		fmt.Sprintf("LogExtraFields=SIDEYARD_RELEASE=%d", s.Release),
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ProtectHome=yes",
		"PrivateTmp=yes",
		"PrivateDevices=yes",
		"ProtectKernelTunables=yes",
		"ProtectKernelModules=yes",
		"ProtectControlGroups=yes",
		"RestrictSUIDSGID=yes",
		"ReadWritePaths=" + s.Project.DataDir(),
		"TasksMax=512",
	}
	if s.MemoryMB > 0 {
		props = append(props, fmt.Sprintf("MemoryMax=%dM", s.MemoryMB))
	}
	if s.CPUPercent > 0 {
		props = append(props, fmt.Sprintf("CPUQuota=%d%%", s.CPUPercent))
	}
	args := []string{
		"--unit=" + unit,
		fmt.Sprintf("--description=Sideyard %s release #%d", s.Project.Name, s.Release),
		"--slice=sideyard.slice",
		"--collect",
		"--quiet",
		"--working-directory=" + s.Project.DataDir(),
	}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	args = append(args, "--", s.Binary)
	return append(args, s.Args...)
}

// encodeEnvFile writes KEY="value" lines. Inside double quotes systemd
// recognises the escapes \" \\ \` \$ and keeps newlines, so any value
// round-trips exactly.
func encodeEnvFile(env []string) []byte {
	var b strings.Builder
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		b.WriteString(k)
		b.WriteString(`="`)
		for _, c := range v {
			if strings.ContainsRune("\\\"`$", c) {
				b.WriteByte('\\')
			}
			b.WriteRune(c)
		}
		b.WriteString("\"\n")
	}
	return []byte(b.String())
}

func envFilePath(p ProjectRef, unit string) string {
	return filepath.Join(p.Dir, "run", strings.TrimSuffix(unit, ".service")+".env")
}

func (r *Systemd) Adopt(p ProjectRef, id string) (Instance, bool) {
	props, err := showUnit(id, "LoadState", "ActiveState")
	if err != nil || props["LoadState"] != "loaded" {
		return nil, false
	}
	switch props["ActiveState"] {
	case "active", "activating", "reloading":
		return &unitInstance{unit: id, envPath: envFilePath(p, id)}, true
	}
	return nil, false
}

func (r *Systemd) Sweep(keep map[string]bool) {
	out, err := exec.Command("systemctl", "list-units", "--all", "--plain", "--no-legend", "--type=service", r.unitPrefix()+"*").Output()
	if err == nil {
		for _, unit := range parseUnitList(out) {
			if !keep[unit] {
				exec.Command("systemctl", "stop", unit).Run()
			}
		}
	}
	envs, _ := filepath.Glob(filepath.Join(r.dataDir, "projects", "*", "run", "*.env"))
	for _, env := range envs {
		if !keep[strings.TrimSuffix(filepath.Base(env), ".env")+".service"] {
			os.Remove(env)
		}
	}
}

// parseUnitList takes the unit names from `systemctl list-units --plain`,
// whose lines may start with a status marker such as "●".
func parseUnitList(out []byte) []string {
	var units []string
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		for _, f := range strings.Fields(sc.Text()) {
			if strings.HasSuffix(f, ".service") {
				units = append(units, f)
				break
			}
		}
	}
	return units
}

func (r *Systemd) Logf(p ProjectRef, format string, args ...any) {
	r.journal.send([][2]string{
		{"MESSAGE", fmt.Sprintf(format, args...)},
		{"SYSLOG_IDENTIFIER", "sideyard"},
		{"PRIORITY", "6"},
		{"SIDEYARD_INSTANCE", r.instance},
		{"SIDEYARD_PROJECT_ID", strconv.FormatInt(p.ID, 10)},
	})
}

func (r *Systemd) Tail(p ProjectRef, n int, match string) ([]string, error) {
	args := []string{"--no-pager", "--quiet", "--output=json", "-n", strconv.Itoa(n),
		"--output-fields=MESSAGE,SIDEYARD_RELEASE,SYSLOG_IDENTIFIER"}
	if match != "" {
		// journald searches the whole journal itself; QuoteMeta makes the
		// pattern (PCRE2) a literal.
		args = append(args, "--grep="+regexp.QuoteMeta(match), "--case-sensitive=false")
	}
	args = append(args, "SIDEYARD_INSTANCE="+r.instance, "SIDEYARD_PROJECT_ID="+strconv.FormatInt(p.ID, 10))
	out, err := exec.Command("journalctl", args...).Output()
	var exit *exec.ExitError
	if match != "" && errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0 {
		return []string{}, nil // nothing matched
	}
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	return parseJournal(out), nil
}

func (r *Systemd) Remove(p ProjectRef) error {
	out, err := exec.Command("userdel", r.user(p)).CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 6 { // no such user
		return nil
	}
	if err != nil {
		return fmt.Errorf("userdel: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unitPrefix starts the name of every unit this installation runs:
// sy-<instance>-<project>-r<release>-<id>.service.
func (r *Systemd) unitPrefix() string {
	return "sy-" + r.instance + "-"
}

func (r *Systemd) user(p ProjectRef) string {
	return unixUser(r.instance, p)
}

// unixUser is a project's user and group name, sy<instance>-<ID>-<name>,
// within the 32-character limit. The ID keeps truncated names unique.
func unixUser(instance string, p ProjectRef) string {
	name := fmt.Sprintf("sy%s-%d-%s", instance, p.ID, p.Name)
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}

func lookupIDs(name string) (int, int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	return uid, gid, nil
}

type unitInstance struct {
	unit    string
	envPath string
}

func (u *unitInstance) ID() string { return u.unit }

func (u *unitInstance) Status() Status {
	props, err := showUnit(u.unit, "LoadState", "ActiveState", "SubState", "MainPID", "NRestarts",
		"ExecMainStartTimestampMonotonic", "MemoryCurrent", "CPUUsageNSec")
	if err != nil {
		return Status{State: Stopped}
	}
	return statusFromProps(props, bootTime())
}

func (u *unitInstance) Stop(timeout time.Duration) {
	// systemd itself escalates to SIGKILL after the unit's TimeoutStopSec.
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "systemctl", "stop", u.unit).Run()
	os.Remove(u.envPath)
}

func statusFromProps(props map[string]string, boot time.Time) Status {
	st := Status{State: Stopped}
	if props["LoadState"] == "not-found" {
		return st
	}
	switch props["ActiveState"] {
	case "active", "reloading":
		st.State = Running
		if props["SubState"] != "running" && props["SubState"] != "reload" {
			st.State = Crashed
		}
	case "activating":
		st.State = Running
		if props["SubState"] == "auto-restart" {
			st.State = Crashed
		}
	case "failed":
		st.State = Crashed
	}
	st.PID, _ = strconv.Atoi(props["MainPID"])
	st.Restarts, _ = strconv.Atoi(props["NRestarts"])
	st.MemoryBytes, _ = strconv.ParseUint(props["MemoryCurrent"], 10, 64)
	st.CPUNanos, _ = strconv.ParseUint(props["CPUUsageNSec"], 10, 64)
	if us, err := strconv.ParseInt(props["ExecMainStartTimestampMonotonic"], 10, 64); err == nil && us > 0 && !boot.IsZero() {
		st.StartedAt = boot.Add(time.Duration(us) * time.Microsecond)
	}
	return st
}

func showUnit(unit string, props ...string) (map[string]string, error) {
	out, err := exec.Command("systemctl", "show", unit, "--property="+strings.Join(props, ",")).Output()
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

var (
	bootOnce sync.Once
	boot     time.Time
)

// bootTime converts systemd's monotonic timestamps to wall-clock time.
func bootTime() time.Time {
	bootOnce.Do(func() {
		b, err := os.ReadFile("/proc/stat")
		if err != nil {
			return
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			if rest, ok := strings.CutPrefix(line, "btime "); ok {
				if sec, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64); err == nil {
					boot = time.Unix(sec, 0)
				}
			}
		}
	})
	return boot
}
