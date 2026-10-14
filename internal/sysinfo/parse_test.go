package sysinfo

import (
	"testing"
)

func TestParseCPU(t *testing.T) {
	c, ok := parseCPU("cpu  100 5 50 800 20 3 2 10 0 0\ncpu0 1 2 3 4\nbtime 1791900000\n")
	if !ok {
		t.Fatal("not parsed")
	}
	if c.total() != 990 || c.busy() != 160 || c.iowait != 20 || c.steal != 10 {
		t.Fatalf("%+v total=%d busy=%d", c, c.total(), c.busy())
	}
	if bt := parseBootTime("cpu  1 2 3 4\nbtime 1791900000\nprocesses 5\n"); bt != 1791900000 {
		t.Fatalf("btime %d", bt)
	}
}

func TestParseMeminfoAndLoad(t *testing.T) {
	m := parseMeminfo("MemTotal:       32847516 kB\nMemAvailable:   14836644 kB\nHugePages_Total:       0\n")
	if m["MemTotal"] != 32847516<<10 || m["MemAvailable"] != 14836644<<10 || m["HugePages_Total"] != 0 {
		t.Fatalf("%v", m)
	}
	var s Sample
	parseLoadavg("0.52 0.41 0.30 2/613 43012\n", &s)
	if s.Load1 != 0.52 || s.Load5 != 0.41 || s.Load15 != 0.30 || s.Processes != 613 {
		t.Fatalf("%+v", s)
	}
}

func TestParseDiskstats(t *testing.T) {
	text := `   7       0 loop0 100 0 4000 10 0 0 0 0 0 10 10 0 0 0 0
 259       0 nvme0n1 5000 10 200000 900 7000 30 300000 1200 0 1500 2100 0 0 0 0
 259       1 nvme0n1p1 4900 10 199000 880 6900 30 299000 1100 0 1400 2000 0 0 0 0
   8       0 sda 10 0 80 1 20 0 160 2 0 3 3 0 0 0 0
 253       0 dm-0 9 0 70 1 19 0 150 2 0 3 3 0 0 0 0`
	r, w := parseDiskstats(text, wholeDisk([]string{"loop0", "nvme0n1", "sda", "dm-0"}))
	if r != (200000+80)*512 || w != (300000+160)*512 {
		t.Fatalf("read %d written %d", r, w)
	}
}

func TestParseNetDev(t *testing.T) {
	text := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9000 10 0 0 0 0 0 0 9000 10 0 0 0 0 0 0
  eth0: 123456 100 0 0 0 0 0 0 654321 90 0 0 0 0 0 0
docker0: 500 5 0 0 0 0 0 0 600 6 0 0 0 0 0 0`
	rx, tx := parseNetDev(text, func(i string) bool { return i == "eth0" })
	if rx != 123456 || tx != 654321 {
		t.Fatalf("rx %d tx %d", rx, tx)
	}
}

func TestParseMounts(t *testing.T) {
	text := `/dev/root / ext4 rw,relatime,discard 0 0
proc /proc proc rw 0 0
/dev/nvme0n1p15 /boot/efi vfat rw 0 0
/dev/nvme1n1 /mnt/datadisk ext4 rw 0 0
/dev/nvme1n1 /mnt/datadisk/docker/bind ext4 rw 0 0
/dev/loop3 /snap/core22/1380 squashfs ro 0 0
/dev/sdb1 /mnt/with\040space xfs rw 0 0
overlay /var/lib/docker/overlay2/x/merged overlay rw 0 0`
	got := parseMounts(text)
	want := []Mount{
		{Device: "/dev/root", Path: "/", FSType: "ext4"},
		{Device: "/dev/nvme1n1", Path: "/mnt/datadisk", FSType: "ext4"},
		{Device: "/dev/sdb1", Path: "/mnt/with space", FSType: "xfs"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mount %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if i := MountFor(got, "/mnt/datadisk/Desktop/sideyard/data"); i != 1 {
		t.Errorf("data dir on mount %d", i)
	}
	if i := MountFor(got, "/var/lib/sideyard"); i != 0 {
		t.Errorf("/var/lib on mount %d", i)
	}
	if i := MountFor(got, "/mnt/datadiskette"); i != 0 {
		t.Errorf("prefix without a slash matched mount %d", i)
	}
}

func TestParseHostFiles(t *testing.T) {
	if v := parseOSRelease("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n"); v != "Ubuntu 24.04.1 LTS" {
		t.Errorf("os %q", v)
	}
	if v := parseCPUModel("processor\t: 0\nmodel name\t: AMD EPYC  7763 64-Core Processor\n"); v != "AMD EPYC 7763 64-Core Processor" {
		t.Errorf("cpu %q", v)
	}
}
