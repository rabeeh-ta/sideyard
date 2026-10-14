package sysinfo

import (
	"bufio"
	"slices"
	"strconv"
	"strings"
)

// The parsers below take the text of a /proc file, so they're testable on
// any OS.

// parseCPU reads the aggregate "cpu" line of /proc/stat.
func parseCPU(stat string) (cpuTimes, bool) {
	line, _, _ := strings.Cut(stat, "\n")
	f := strings.Fields(line)
	if len(f) < 5 || f[0] != "cpu" {
		return cpuTimes{}, false
	}
	v := make([]uint64, 8)
	for i := range min(len(f)-1, 8) {
		v[i], _ = strconv.ParseUint(f[i+1], 10, 64)
	}
	return cpuTimes{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}, true
}

// parseBootTime reads the "btime" line of /proc/stat, in Unix seconds.
func parseBootTime(stat string) int64 {
	for line := range strings.SplitSeq(stat, "\n") {
		if rest, ok := strings.CutPrefix(line, "btime "); ok {
			n, _ := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
			return n
		}
	}
	return 0
}

// parseMeminfo returns /proc/meminfo's figures in bytes, by name.
func parseMeminfo(text string) map[string]uint64 {
	out := map[string]uint64{}
	for line := range strings.SplitSeq(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		n, _ := strconv.ParseUint(f[0], 10, 64)
		if len(f) > 1 && f[1] == "kB" {
			n <<= 10
		}
		out[name] = n
	}
	return out
}

// parseLoadavg reads /proc/loadavg: three load averages, then
// running/total processes.
func parseLoadavg(text string, s *Sample) {
	f := strings.Fields(text)
	if len(f) < 4 {
		return
	}
	s.Load1, _ = strconv.ParseFloat(f[0], 64)
	s.Load5, _ = strconv.ParseFloat(f[1], 64)
	s.Load15, _ = strconv.ParseFloat(f[2], 64)
	if _, total, ok := strings.Cut(f[3], "/"); ok {
		s.Processes, _ = strconv.Atoi(total)
	}
}

// parseDiskstats sums bytes read and written by the devices include
// accepts. The kernel counts in 512-byte sectors whatever the disk's own
// sector size.
func parseDiskstats(text string, include func(dev string) bool) (read, written uint64) {
	for line := range strings.SplitSeq(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || !include(f[2]) {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64)
		w, _ := strconv.ParseUint(f[9], 10, 64)
		read += r * 512
		written += w * 512
	}
	return read, written
}

// wholeDisk accepts physical disks and rejects partitions' pseudo devices
// and the virtual ones whose I/O is already counted on a disk below them.
func wholeDisk(blockDevices []string) func(string) bool {
	return func(dev string) bool {
		if !slices.Contains(blockDevices, dev) {
			return false // a partition: /sys/block lists only whole devices
		}
		for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "sr", "fd", "nbd"} {
			if strings.HasPrefix(dev, p) {
				return false
			}
		}
		return true
	}
}

// parseNetDev sums bytes received and sent by the interfaces include
// accepts.
func parseNetDev(text string, include func(iface string) bool) (rx, tx uint64) {
	for line := range strings.SplitSeq(text, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 || !include(strings.TrimSpace(name)) {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		t, _ := strconv.ParseUint(f[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx
}

// diskFS are the filesystems worth showing; the rest are virtual (proc,
// tmpfs, overlay), read-only images (squashfs) or boot partitions (vfat).
var diskFS = []string{"ext2", "ext3", "ext4", "xfs", "btrfs", "zfs", "f2fs", "bcachefs", "jfs", "reiserfs"}

// parseMounts lists the disk-backed filesystems in /proc/self/mounts, one
// per device: bind mounts and btrfs subvolumes keep the shortest path.
func parseMounts(text string) []Mount {
	var out []Mount
	seen := map[string]int{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || !slices.Contains(diskFS, f[2]) {
			continue
		}
		m := Mount{Device: unescapeMount(f[0]), Path: unescapeMount(f[1]), FSType: f[2]}
		if strings.HasPrefix(m.Path, "/snap/") || strings.HasPrefix(m.Path, "/var/lib/docker/") {
			continue
		}
		if i, ok := seen[m.Device]; ok {
			if len(m.Path) < len(out[i].Path) {
				out[i] = m
			}
			continue
		}
		seen[m.Device] = len(out)
		out = append(out, m)
	}
	return out
}

// MountFor is the mount holding path: the longest mount path it's under.
func MountFor(mounts []Mount, path string) int {
	best := -1
	for i, m := range mounts {
		if path == m.Path || m.Path == "/" || strings.HasPrefix(path, m.Path+"/") {
			if best < 0 || len(m.Path) > len(mounts[best].Path) {
				best = i
			}
		}
	}
	return best
}

// parseOSRelease returns PRETTY_NAME from /etc/os-release.
func parseOSRelease(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// parseCPUModel returns the first "model name" in /proc/cpuinfo. ARM
// kernels don't report one.
func parseCPUModel(text string) string {
	for line := range strings.SplitSeq(text, "\n") {
		if name, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "model name" {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}
