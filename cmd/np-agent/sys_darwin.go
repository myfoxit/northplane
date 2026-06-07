//go:build darwin

package main

import (
	"encoding/binary"
	"syscall"
	"unsafe"
)

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
