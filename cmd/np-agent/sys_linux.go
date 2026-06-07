//go:build linux

package main

// darwinLoad is a stub on linux (loadAvg uses /proc/loadavg there).
func darwinLoad() (float64, bool) { return 0, false }

// darwinMem is a stub on linux (memUsage uses /proc/meminfo there).
func darwinMem() (float64, uint64, uint64, bool) { return 0, 0, 0, false }
