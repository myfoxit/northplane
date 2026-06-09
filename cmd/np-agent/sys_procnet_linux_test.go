//go:build linux

package main

import "testing"

func TestParseProcNetDev(t *testing.T) {
	sample := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 8324512   12345    0    0    0     0          0         0  8324512   12345    0    0    0     0       0          0
  eth0: 994872034 7654321    0    0    0     0          0      1024 482910238 6543210    0    0    0     0       0          0`
	got := parseProcNetDev(sample)
	if len(got) != 1 {
		t.Fatalf("want eth0 only (lo excluded), got %+v", got)
	}
	if got[0].Name != "eth0" || got[0].RxBytes != 994872034 || got[0].TxBytes != 482910238 {
		t.Fatalf("eth0 parse: %+v", got[0])
	}
}
