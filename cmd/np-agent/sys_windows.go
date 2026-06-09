//go:build windows

// Windows support (NCPA feature parity): memory via GlobalMemoryStatusEx,
// disk via GetDiskFreeSpaceExW, processes via Toolhelp32 snapshot, CPU%
// via GetSystemTimes deltas. Plain syscall against kernel32 — no cgo, no
// extra dependency. Windows has no load average; cpuPercent() fills that
// role in collect() and /v1/metrics.
package main

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx     = kernel32.NewProc("GlobalMemoryStatusEx")
	procGetDiskFreeSpaceExW      = kernel32.NewProc("GetDiskFreeSpaceExW")
	procGetSystemTimes           = kernel32.NewProc("GetSystemTimes")
	procCreateToolhelp32Snapshot = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32FirstW          = kernel32.NewProc("Process32FirstW")
	procProcess32NextW           = kernel32.NewProc("Process32NextW")
)

// loadAvg: Windows has no load average; the cpu service covers it.
func loadAvg() (float64, bool) { return 0, false }

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memUsage() (usedPct float64, total, avail uint64, ok bool) {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r == 0 || ms.TotalPhys == 0 {
		return 0, 0, 0, false
	}
	return 100 * float64(ms.TotalPhys-ms.AvailPhys) / float64(ms.TotalPhys),
		ms.TotalPhys, ms.AvailPhys, true
}

func diskUsage(mount string) (float64, uint64, bool) {
	p, err := syscall.UTF16PtrFromString(mount)
	if err != nil {
		return 0, 0, false
	}
	var freeAvail, totalBytes, totalFree uint64
	r, _, _ := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)))
	if r == 0 || totalBytes == 0 {
		return 0, 0, false
	}
	usedPct := 100 * float64(totalBytes-freeAvail) / float64(totalBytes)
	return usedPct, freeAvail, true
}

const th32csSnapProcess = 0x00000002

type processEntry32 struct {
	Size            uint32
	CntUsage        uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	CntThreads      uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [260]uint16
}

// procCount counts processes via a Toolhelp32 snapshot. Windows exposes
// no "running" scheduler state per process; running stays 0.
func procCount() (total, running int, ok bool) {
	snap, _, _ := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	h := syscall.Handle(snap)
	if h == syscall.InvalidHandle {
		return 0, 0, false
	}
	defer syscall.CloseHandle(h)
	var pe processEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if r, _, _ := procProcess32FirstW.Call(uintptr(h), uintptr(unsafe.Pointer(&pe))); r == 0 {
		return 0, 0, false
	}
	for {
		total++
		if r, _, _ := procProcess32NextW.Call(uintptr(h), uintptr(unsafe.Pointer(&pe))); r == 0 {
			break
		}
	}
	return total, 0, total > 0
}

// netCounters: not collected on Windows in v1 (no stable stdlib-only
// counter source); the network service is simply absent on this platform.
func netCounters() ([]netCounter, bool) { return nil, false }

var cpuMu sync.Mutex
var lastIdle, lastKernel, lastUser uint64

// cpuPercent returns total CPU utilisation since the previous call
// (GetSystemTimes deltas; kernel time includes idle time). The first
// call primes the baseline and reports ok=false.
func cpuPercent() (float64, bool) {
	var idleFT, kernelFT, userFT uint64
	r, _, _ := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idleFT)),
		uintptr(unsafe.Pointer(&kernelFT)),
		uintptr(unsafe.Pointer(&userFT)))
	if r == 0 {
		return 0, false
	}
	cpuMu.Lock()
	defer cpuMu.Unlock()
	primed := lastKernel != 0 || lastUser != 0
	dIdle := idleFT - lastIdle
	dKernel := kernelFT - lastKernel
	dUser := userFT - lastUser
	lastIdle, lastKernel, lastUser = idleFT, kernelFT, userFT
	total := dKernel + dUser
	if !primed || total == 0 {
		return 0, false
	}
	busy := total - dIdle
	return 100 * float64(busy) / float64(total), true
}
