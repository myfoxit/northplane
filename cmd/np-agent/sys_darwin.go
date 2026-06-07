//go:build darwin

package main

import (
	"encoding/binary"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// darwinMem reports host RAM usage on macOS. Total comes from hw.memsize;
// "available" sums the reclaimable VM page classes (free + inactive +
// speculative + purgeable) reported by vm_stat. The per-class page counts
// are NOT available as sysctls (only vm.page_free_count is) — they come
// from the Mach VM statistics, which vm_stat exposes without cgo. If
// parsing yields nothing we report ok=false rather than a misleading
// figure (a wrong "99% used" would fire bogus CRITICAL alerts).
func darwinMem() (usedPct float64, total, avail uint64, ok bool) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return 0, 0, 0, false
	}
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, 0, 0, false
	}
	pagesize := uint64(4096)
	if p, e := unix.SysctlUint32("hw.pagesize"); e == nil && p > 0 {
		pagesize = uint64(p)
	}
	counts := map[string]uint64{}
	for _, line := range strings.Split(string(out), "\n") {
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		val := strings.TrimSuffix(strings.TrimSpace(line[i+1:]), ".")
		if v, e := strconv.ParseUint(val, 10, 64); e == nil {
			counts[strings.TrimSpace(line[:i])] = v
		}
	}
	reclaimable := counts["Pages free"] + counts["Pages inactive"] +
		counts["Pages speculative"] + counts["Pages purgeable"]
	if reclaimable == 0 {
		return 0, 0, 0, false // parse failed — better silent than wrong
	}
	avail = reclaimable * pagesize
	if avail > total {
		avail = total
	}
	return 100 * float64(total-avail) / float64(total), total, avail, true
}

// darwinLoad reads vm.loadavg via sysctl.
func darwinLoad() (float64, bool) {
	raw, err := syscall.Sysctl("vm.loadavg")
	if err != nil || len(raw) < 12 {
		return 0, false
	}
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }
	b := []byte(raw)
	// Sysctl strips trailing NULs; ensure length
	for len(b) < int(unsafe.Sizeof(uint32(0)))*4 {
		b = append(b, 0)
	}
	load1 := binary.LittleEndian.Uint32(b[0:4])
	const fscale = 2048.0 // standard FSCALE on darwin
	return float64(load1) / fscale, true
}
