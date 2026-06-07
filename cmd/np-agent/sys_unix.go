//go:build darwin || linux

package main

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// loadAvg reads the 1-minute load average.
func loadAvg() (float64, bool) {
	// linux: /proc/loadavg
	if raw, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(raw))
		if len(fields) > 0 {
			if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
				return v, true
			}
		}
	}
	// darwin: sysctl via Getloadavg syscall is unexported; use sysctlbyname
	if v, ok := darwinLoad(); ok {
		return v, true
	}
	return 0, false
}

// memUsage returns used %, total bytes and available bytes for host RAM.
func memUsage() (usedPct float64, total, avail uint64, ok bool) {
	if raw, err := os.ReadFile("/proc/meminfo"); err == nil { // linux
		var t, a uint64
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				t = v * 1024
			case "MemAvailable:":
				a = v * 1024
			}
		}
		if t > 0 {
			return 100 * float64(t-a) / float64(t), t, a, true
		}
	}
	return darwinMem() // darwin path (stub on linux)
}

// diskUsage returns used % and free bytes for a mount point.
func diskUsage(mount string) (float64, uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return 0, 0, false
	}
	total := uint64(st.Blocks) * uint64(st.Bsize)
	free := uint64(st.Bavail) * uint64(st.Bsize)
	if total == 0 {
		return 0, 0, false
	}
	usedPct := 100 * float64(total-free) / float64(total)
	return usedPct, free, true
}
