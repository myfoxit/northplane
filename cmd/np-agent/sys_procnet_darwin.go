//go:build darwin

package main

import (
	"os/exec"
	"strconv"
	"strings"
)

// procCount returns the total and running process counts (via ps — the
// stable BSD interface; mach host calls are not worth the cgo).
func procCount() (total, running int, ok bool) {
	out, err := exec.Command("/bin/ps", "-axo", "state=").Output()
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		if line[0] == 'R' {
			running++
		}
	}
	return total, running, total > 0
}

// netCounters returns cumulative per-interface rx/tx byte counters
// (loopback excluded), parsed from `netstat -ibn` link lines.
func netCounters() ([]netCounter, bool) {
	out, err := exec.Command("/usr/sbin/netstat", "-ibn").Output()
	if err != nil {
		return nil, false
	}
	return parseNetstatIB(string(out)), true
}

// parseNetstatIB extracts the per-link totals: only "<Link#N>" rows carry
// interface byte counters. The Address column may be absent, so byte
// columns are addressed from the line end (… Ipkts Ierrs Ibytes Opkts
// Oerrs Obytes Coll).
func parseNetstatIB(content string) []netCounter {
	var out []netCounter
	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "<Link#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 7 {
			continue
		}
		name := f[0]
		if strings.HasPrefix(name, "lo") {
			continue
		}
		rx, err1 := strconv.ParseUint(f[len(f)-5], 10, 64)
		tx, err2 := strconv.ParseUint(f[len(f)-2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, netCounter{Name: name, RxBytes: rx, TxBytes: tx})
	}
	return out
}

// cpuPercent is unavailable on this platform (load average covers it).
func cpuPercent() (float64, bool) { return 0, false }
