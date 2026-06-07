//go:build linux

package main

// darwinLoad is a stub on linux (loadAvg uses /proc/loadavg there).
func darwinLoad() (float64, bool) { return 0, false }
