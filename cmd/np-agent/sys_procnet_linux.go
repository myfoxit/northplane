//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"
)

// procCount returns the total and running process counts.
func procCount() (total, running int, ok bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, false
	}
	for _, e := range entries {
		if e.IsDir() && isAllDigits(e.Name()) {
			total++
		}
	}
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, found := strings.CutPrefix(line, "procs_running "); found {
				running, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
	}
	return total, running, total > 0
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// netCounters returns cumulative per-interface rx/tx byte counters
// (loopback excluded).
func netCounters() ([]netCounter, bool) {
	raw, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, false
	}
	return parseProcNetDev(string(raw)), true
}

// parseProcNetDev parses /proc/net/dev content ("iface: rx_bytes … tx_bytes …").
func parseProcNetDev(content string) []netCounter {
	var out []netCounter
	for _, line := range strings.Split(content, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "" || name == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			continue
		}
		rx, err1 := strconv.ParseUint(f[0], 10, 64)
		tx, err2 := strconv.ParseUint(f[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, netCounter{Name: name, RxBytes: rx, TxBytes: tx})
	}
	return out
}

// cpuPercent is unavailable on this platform (load average covers it).
func cpuPercent() (float64, bool) { return 0, false }
