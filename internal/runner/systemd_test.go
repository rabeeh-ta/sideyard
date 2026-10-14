package runner

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestEncodeEnvFile(t *testing.T) {
	got := string(encodeEnvFile([]string{
		"PLAIN=hello world",
		`TRICKY=a"b\c$d` + "`e",
		"MULTI=line1\nline2",
		"EMPTY=",
		"EQUALS=a=b",
	}))
	want := `PLAIN="hello world"
TRICKY="a\"b\\c\$d` + "\\`" + `e"
MULTI="line1
line2"
EMPTY=""
EQUALS="a=b"
`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRunArgs(t *testing.T) {
	s := Spec{
		Project:    ProjectRef{ID: 3, Name: "maker", Dir: "/var/lib/sideyard/projects/maker"},
		Release:    7,
		Binary:     "/var/lib/sideyard/projects/maker/releases/7/app",
		Args:       []string{"serve", "--http=127.0.0.1:4000"},
		MemoryMB:   512,
		CPUPercent: 50,
	}
	args := runArgs(s, "a3f9", "sy-a3f9-maker-r7-x.service", "/run/env", "sya3f9-3-maker")
	for _, want := range []string{
		"--unit=sy-a3f9-maker-r7-x.service",
		"--slice=sideyard.slice",
		"--working-directory=/var/lib/sideyard/projects/maker/data",
		"--property=User=sya3f9-3-maker",
		"--property=EnvironmentFile=/run/env",
		"--property=ReadWritePaths=/var/lib/sideyard/projects/maker/data",
		"--property=LogExtraFields=SIDEYARD_INSTANCE=a3f9",
		"--property=LogExtraFields=SIDEYARD_PROJECT_ID=3",
		"--property=LogExtraFields=SIDEYARD_RELEASE=7",
		"--property=MemoryMax=512M",
		"--property=CPUQuota=50%",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %s", want)
		}
	}
	tail := args[slices.Index(args, "--"):]
	if strings.Join(tail, " ") != "-- /var/lib/sideyard/projects/maker/releases/7/app serve --http=127.0.0.1:4000" {
		t.Errorf("command = %q", tail)
	}
	for _, a := range args {
		if strings.Contains(a, "Environment=") && !strings.Contains(a, "EnvironmentFile=") {
			t.Errorf("secrets must not be passed as unit properties: %s", a)
		}
	}
}

func TestUnixUser(t *testing.T) {
	if got := unixUser("a3f9", ProjectRef{ID: 3, Name: "maker"}); got != "sya3f9-3-maker" {
		t.Errorf("got %q", got)
	}
	long := unixUser("a3f9", ProjectRef{ID: 12, Name: strings.Repeat("a", 40)})
	if len(long) != 32 || !strings.HasPrefix(long, "sya3f9-12-") {
		t.Errorf("got %q (%d chars)", long, len(long))
	}
}

func TestStatusFromProps(t *testing.T) {
	boot := time.Unix(1_700_000_000, 0)
	cases := []struct {
		props map[string]string
		want  State
	}{
		{map[string]string{"LoadState": "loaded", "ActiveState": "active", "SubState": "running"}, Running},
		{map[string]string{"LoadState": "loaded", "ActiveState": "activating", "SubState": "auto-restart"}, Crashed},
		{map[string]string{"LoadState": "loaded", "ActiveState": "failed", "SubState": "failed"}, Crashed},
		{map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead"}, Stopped},
		{map[string]string{"LoadState": "not-found", "ActiveState": "inactive"}, Stopped},
	}
	for _, c := range cases {
		if got := statusFromProps(c.props, boot).State; got != c.want {
			t.Errorf("%v: got %s, want %s", c.props, got, c.want)
		}
	}
	st := statusFromProps(map[string]string{
		"LoadState": "loaded", "ActiveState": "active", "SubState": "running",
		"MainPID": "4242", "NRestarts": "2", "MemoryCurrent": "15728640", "CPUUsageNSec": "1500000000",
		"ExecMainStartTimestampMonotonic": "5000000",
	}, boot)
	if st.PID != 4242 || st.Restarts != 2 || st.MemoryBytes != 15728640 || st.CPUNanos != 1_500_000_000 || !st.StartedAt.Equal(boot.Add(5*time.Second)) {
		t.Errorf("got %+v", st)
	}
	if st := statusFromProps(map[string]string{"LoadState": "loaded", "ActiveState": "active", "SubState": "running", "MemoryCurrent": "[not set]"}, boot); st.MemoryBytes != 0 {
		t.Errorf("unset memory should be 0, got %d", st.MemoryBytes)
	}
}

func TestParseJournal(t *testing.T) {
	out := []byte(`{"__REALTIME_TIMESTAMP":"1791970000000000","MESSAGE":"listening on 4000","SIDEYARD_RELEASE":"7"}
{"__REALTIME_TIMESTAMP":"1791970001000000","MESSAGE":"release #7: live","SYSLOG_IDENTIFIER":"sideyard"}
{"__REALTIME_TIMESTAMP":"1791970002000000","MESSAGE":[104,105,255],"SIDEYARD_RELEASE":"7"}
{"__REALTIME_TIMESTAMP":"1791970003000000","MESSAGE":null,"SIDEYARD_RELEASE":"7"}
`)
	got := parseJournal(out)
	want := []string{
		"2026-10-14 09:26:40 [#7] listening on 4000",
		"2026-10-14 09:26:41 [sideyard] release #7: live",
		"2026-10-14 09:26:42 [#7] hi�",
		"2026-10-14 09:26:43 [#7] [message too large to show]",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestParseUnitList(t *testing.T) {
	out := []byte("sy-maker-r7-abc.service loaded active running Sideyard maker release #7\n● sy-notes-r2-def.service loaded failed failed Sideyard notes release #2\n")
	got := parseUnitList(out)
	if !slices.Equal(got, []string{"sy-maker-r7-abc.service", "sy-notes-r2-def.service"}) {
		t.Fatalf("got %q", got)
	}
}
