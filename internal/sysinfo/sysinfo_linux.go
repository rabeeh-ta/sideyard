package sysinfo

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func readCounters() counters {
	var c counters
	c.cpu, _ = parseCPU(readFile("/proc/stat"))
	var disks []string
	if entries, err := os.ReadDir("/sys/block"); err == nil {
		for _, e := range entries {
			disks = append(disks, e.Name())
		}
	}
	c.diskRead, c.diskWrite = parseDiskstats(readFile("/proc/diskstats"), wholeDisk(disks))
	c.netRx, c.netTx = parseNetDev(readFile("/proc/net/dev"), networkInterfaces())
	return c
}

// networkInterfaces accepts the interfaces backed by a device (a NIC,
// virtual or not), so bridges, veths and tunnels aren't counted twice. If
// there are none, it accepts everything but loopback.
func networkInterfaces() func(string) bool {
	entries, _ := os.ReadDir("/sys/class/net")
	var devices []string
	for _, e := range entries {
		if _, err := os.Stat("/sys/class/net/" + e.Name() + "/device"); err == nil {
			devices = append(devices, e.Name())
		}
	}
	return func(iface string) bool {
		if len(devices) == 0 {
			return iface != "lo"
		}
		for _, d := range devices {
			if d == iface {
				return true
			}
		}
		return false
	}
}

func readLoadAndMemory(s *Sample) {
	parseLoadavg(readFile("/proc/loadavg"), s)
	m := parseMeminfo(readFile("/proc/meminfo"))
	s.MemTotalBytes = m["MemTotal"]
	s.MemAvailBytes = m["MemAvailable"]
	s.MemCachedBytes = m["Cached"] + m["Buffers"] + m["SReclaimable"]
	s.SwapTotalBytes = m["SwapTotal"]
	s.SwapFreeBytes = m["SwapFree"]
}

func readHost(h *Host) {
	if v := parseOSRelease(readFile("/etc/os-release")); v != "" {
		h.OS = v
	}
	h.Kernel = strings.TrimSpace(readFile("/proc/sys/kernel/osrelease"))
	h.CPUModel = parseCPUModel(readFile("/proc/cpuinfo"))
	vendor := strings.TrimSpace(readFile("/sys/class/dmi/id/sys_vendor"))
	product := strings.TrimSpace(readFile("/sys/class/dmi/id/product_name"))
	if strings.HasPrefix(product, vendor) {
		vendor = ""
	}
	h.Machine = strings.TrimSpace(vendor + " " + product)
	if bt := parseBootTime(readFile("/proc/stat")); bt > 0 {
		h.BootedAt = time.Unix(bt, 0).UTC()
	}
}

// Mounts lists the disk-backed filesystems with their space.
func Mounts() []Mount {
	var out []Mount
	for _, m := range parseMounts(readFile("/proc/self/mounts")) {
		if statMount(&m) && m.TotalBytes > 0 {
			out = append(out, m)
		}
	}
	return out
}

// ProcessRSS returns the resident memory of a process in bytes.
func ProcessRSS(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				kb, _ := strconv.ParseUint(fields[0], 10, 64)
				return kb << 10
			}
		}
	}
	return 0
}

// ProcessCPU returns the CPU time a process has used, in nanoseconds.
func ProcessCPU(pid int) uint64 {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The command name in parentheses may contain spaces; fields after it
	// start with the state (field 3). utime and stime are fields 14 and 15.
	s := string(b)
	fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(fields) < 13 {
		return 0
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	const clockTicks = 100 // USER_HZ, 100 on every mainstream Linux
	return (utime + stime) * (1e9 / clockTicks)
}
