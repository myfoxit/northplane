//go:build darwin

package main

import "testing"

func TestParseNetstatIB(t *testing.T) {
	sample := `Name       Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0        16384 <Link#1>                          636462     0  118693290   636462     0  118693290     0
en0        1500  <Link#12>   aa:bb:cc:dd:ee:ff   12760908     0 9952282556  9620054     0 2492015483     0
en0        1500  192.168.1     192.168.1.42      12760908     - 9952282556  9620054     - 2492015483     -
utun0      1380  <Link#18>                              0     0          0        2     0        296     0`
	got := parseNetstatIB(sample)
	if len(got) != 2 {
		t.Fatalf("want en0+utun0 (lo excluded, IP rows excluded), got %+v", got)
	}
	if got[0].Name != "en0" || got[0].RxBytes != 9952282556 || got[0].TxBytes != 2492015483 {
		t.Fatalf("en0 parse: %+v", got[0])
	}
	if got[1].Name != "utun0" || got[1].TxBytes != 296 {
		t.Fatalf("utun0 parse: %+v", got[1])
	}
}
