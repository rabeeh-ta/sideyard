//go:build !linux

package sysinfo

func readCounters() counters { return counters{} }

func readLoadAndMemory(*Sample) {}

func readHost(*Host) {}

// Mounts reports only the root filesystem outside Linux.
func Mounts() []Mount {
	m := Mount{Path: "/"}
	if !statMount(&m) {
		return nil
	}
	return []Mount{m}
}

func ProcessRSS(int) uint64 { return 0 }

func ProcessCPU(int) uint64 { return 0 }
