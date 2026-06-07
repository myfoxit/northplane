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
