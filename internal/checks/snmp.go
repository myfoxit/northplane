package checks

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

func init() {
	register("snmp", checkSNMP)
	register("snmp-walk", checkSNMPWalk)
}

func snmpClient(t Target, a Args, timeout time.Duration) (*gosnmp.GoSNMP, error) {
	host := a.Host(t)
	if host == "" {
		return nil, fmt.Errorf("snmp: no host")
	}
	port := a.Int(161, "p", "port")
	// Accept Nagios-style "host:port" addresses: an explicit -p wins,
	// otherwise the port embedded in the address applies.
	if h, p, err := net.SplitHostPort(host); err == nil {
		if n, perr := strconv.Atoi(p); perr == nil && n > 0 && n < 65536 {
			host = h
			if a.Get("p", "port") == "" {
				port = n
			}
		}
	}
	g := &gosnmp.GoSNMP{
		Target:  host,
		Port:    uint16(port),
		Timeout: timeout,
		Retries: a.Int(1, "retries"),
	}
	switch a.Get("v", "protocol") {
	case "3":
		g.Version = gosnmp.Version3
		g.SecurityModel = gosnmp.UserSecurityModel
		level := a.Get("seclevel")
		usm := &gosnmp.UsmSecurityParameters{UserName: a.Get("user", "U")}
		switch level {
		case "authPriv":
			g.MsgFlags = gosnmp.AuthPriv
			usm.AuthenticationProtocol = snmpAuthProto(a.Get("authproto"))
			usm.AuthenticationPassphrase = a.Get("authpass", "A")
			usm.PrivacyProtocol = snmpPrivProto(a.Get("privproto"))
			usm.PrivacyPassphrase = a.Get("privpass", "X")
		case "authNoPriv":
			g.MsgFlags = gosnmp.AuthNoPriv
			usm.AuthenticationProtocol = snmpAuthProto(a.Get("authproto"))
			usm.AuthenticationPassphrase = a.Get("authpass", "A")
		default:
			g.MsgFlags = gosnmp.NoAuthNoPriv
		}
		g.SecurityParameters = usm
	case "1":
		g.Version = gosnmp.Version1
		g.Community = community(a)
	default:
		g.Version = gosnmp.Version2c
		g.Community = community(a)
	}
	return g, nil
}

func community(a Args) string {
	if c := a.Get("C", "community"); c != "" {
		return c
	}
	return "public"
}

func snmpAuthProto(s string) gosnmp.SnmpV3AuthProtocol {
	switch strings.ToUpper(s) {
	case "MD5":
		return gosnmp.MD5
	case "SHA256":
		return gosnmp.SHA256
	case "SHA512":
		return gosnmp.SHA512
	default:
		return gosnmp.SHA
	}
}

func snmpPrivProto(s string) gosnmp.SnmpV3PrivProtocol {
	switch strings.ToUpper(s) {
	case "DES":
		return gosnmp.DES
	case "AES256":
		return gosnmp.AES256
	default:
		return gosnmp.AES
	}
}

// checkSNMP: get one OID and grade it. Flags: -o OID, -w/-c ranges,
// -l label, --unit, -C community, -v 1|2c|3 (+v3 flags).
func checkSNMP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	oid := a.Get("o", "oid")
	if oid == "" {
		return unknownf("snmp: -o OID required")
	}
	timeout := a.Duration(10*time.Second, "t", "timeout")
	g, err := snmpClient(t, a, timeout)
	if err != nil {
		return unknownf("%v", err)
	}
	if err := g.Connect(); err != nil {
		return criticalf("snmp connect %s: %v", g.Target, err)
	}
	defer g.Conn.Close()

	done := make(chan struct{})
	var pkt *gosnmp.SnmpPacket
	var qerr error
	go func() {
		pkt, qerr = g.Get([]string{oid})
		close(done)
	}()
	select {
	case <-ctx.Done():
		return unknownf("snmp: cancelled")
	case <-done:
	}
	if qerr != nil {
		return criticalf("snmp get %s %s: %v", g.Target, oid, qerr)
	}
	if len(pkt.Variables) == 0 {
		return criticalf("snmp: empty response for %s", oid)
	}
	v := pkt.Variables[0]
	label := a.Get("l", "label")
	if label == "" {
		label = "value"
	}
	switch v.Type {
	case gosnmp.OctetString:
		s := string(v.Value.([]byte))
		if expect := a.Get("e", "expect", "s"); expect != "" && !strings.Contains(s, expect) {
			return criticalf("SNMP %s = %q (expected %q)", oid, s, expect)
		}
		return model.StateOK, nagios.Output{Text: fmt.Sprintf("SNMP OK - %s = %q", oid, s)}
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance:
		return criticalf("SNMP: no such OID %s", oid)
	default:
		f, ok := snmpNumeric(v)
		if !ok {
			return unknownf("SNMP %s: non-numeric type %v", oid, v.Type)
		}
		return evalPerf(label, f, a.Get("unit"), a.Get("w", "warning"), a.Get("c", "critical"),
			fmt.Sprintf("%s = %s%s", oid, trimFloat(f), a.Get("unit")))
	}
}

func snmpNumeric(v gosnmp.SnmpPDU) (float64, bool) {
	switch x := v.Value.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// checkSNMPWalk: walk a subtree, grade the row count (e.g. interface
// tables) or match values. Flags: -o base OID, -w/-c on count.
func checkSNMPWalk(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	oid := a.Get("o", "oid")
	if oid == "" {
		return unknownf("snmp-walk: -o OID required")
	}
	timeout := a.Duration(20*time.Second, "t", "timeout")
	g, err := snmpClient(t, a, timeout)
	if err != nil {
		return unknownf("%v", err)
	}
	if err := g.Connect(); err != nil {
		return criticalf("snmp connect %s: %v", g.Target, err)
	}
	defer g.Conn.Close()

	count := 0
	done := make(chan error, 1)
	go func() {
		done <- g.BulkWalk(oid, func(p gosnmp.SnmpPDU) error {
			count++
			return nil
		})
	}()
	select {
	case <-ctx.Done():
		return unknownf("snmp-walk: cancelled")
	case err := <-done:
		if err != nil {
			return criticalf("snmp walk %s %s: %v", g.Target, oid, err)
		}
	}
	return evalPerf("rows", float64(count), "", a.Get("w", "warning"), a.Get("c", "critical"),
		fmt.Sprintf("walk %s returned %d rows", oid, count))
}
