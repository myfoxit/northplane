package checks

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/northplane/northplane/internal/model"
	"github.com/northplane/northplane/internal/nagios"
)

func init() {
	register("icmp", checkICMP)
	register("ping", checkICMP)
	register("tcp", checkTCP)
	register("ssh-banner", checkSSHBanner)
	register("smtp", checkSMTP)
	register("imap", checkIMAP)
	register("ntp", checkNTP)
	register("dns", checkDNS)
}

// checkICMP: native echo. Uses an unprivileged datagram ICMP socket
// (udp4) where the platform allows it, falling back to a privileged raw
// socket when running as root. Flags: -w/-c on RTT ms, -t timeout.
func checkICMP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	if host == "" {
		return unknownf("no address")
	}
	timeout := a.Duration(5*time.Second, "t", "timeout")
	warn := a.Get("w", "warning")
	crit := a.Get("c", "critical")
	if warn == "" {
		warn = "200"
	}
	if crit == "" {
		crit = "1000"
	}

	dst, err := net.ResolveIPAddr("ip4", host)
	if err != nil {
		return criticalf("cannot resolve %s: %v", host, err)
	}

	conn, privileged, err := openICMP()
	if err != nil {
		return unknownf("icmp socket: %v (unprivileged ICMP unavailable — run as root, grant cap_net_raw, or use builtin:tcp)", err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	echo := &icmp.Message{
		Type: ipv4.ICMPTypeEcho, Code: 0,
		Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("northplane-ping")},
	}
	wire, err := echo.Marshal(nil)
	if err != nil {
		return unknownf("marshal: %v", err)
	}
	var dest net.Addr = dst
	if !privileged {
		dest = &net.UDPAddr{IP: dst.IP}
	}
	start := time.Now()
	if _, err := conn.WriteTo(wire, dest); err != nil {
		return criticalf("send to %s: %v", host, err)
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return criticalf("no reply from %s within %s", host, timeout)
		}
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}
		if msg.Type == ipv4.ICMPTypeEchoReply {
			rtt := float64(time.Since(start).Microseconds()) / 1000
			st, out := evalPerf("rta", rtt, "ms", warn, crit,
				fmt.Sprintf("%s rta %.2fms", host, rtt))
			out.Text = strings.Replace(out.Text, "RTA", "PING", 1)
			return st, out
		}
	}
}

func openICMP() (*icmp.PacketConn, bool, error) {
	if conn, err := icmp.ListenPacket("udp4", "0.0.0.0"); err == nil {
		return conn, false, nil
	}
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return nil, false, err
	}
	return conn, true, nil
}

// checkTCP: connect, optional send/expect, optional TLS.
// Flags: -p port, -w/-c connect-time s, -e expect, -s send, --ssl.
func checkTCP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	port := a.Int(0, "p", "port")
	if port == 0 {
		return unknownf("tcp: -p port required")
	}
	timeout := a.Duration(10*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, fmt.Sprint(port))

	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if a.Bool("ssl", "S") {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{
			InsecureSkipVerify: a.Bool("insecure"), ServerName: host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return criticalf("connect to %s failed: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	elapsed := time.Since(start).Seconds()

	if send := a.Get("s", "send"); send != "" {
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
		fmt.Fprintf(conn, "%s\r\n", send)
	}
	if expect := a.Get("e", "expect"); expect != "" {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		if !strings.Contains(string(buf[:n]), expect) {
			return criticalf("unexpected response from %s: %q", addr, firstLine(string(buf[:n])))
		}
	}
	warn := a.Get("w", "warning")
	crit := a.Get("c", "critical")
	return evalPerf("time", elapsed, "s", warn, crit,
		fmt.Sprintf("connected to %s in %.3fs", addr, elapsed))
}

// checkSSHBanner: read the SSH identification string.
func checkSSHBanner(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	port := a.Int(22, "p", "port")
	timeout := a.Duration(10*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return criticalf("connect to %s failed: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return criticalf("no banner from %s: %v", addr, err)
	}
	banner = strings.TrimSpace(banner)
	if !strings.HasPrefix(banner, "SSH-") {
		return criticalf("not an SSH service at %s: %q", addr, firstLine(banner))
	}
	elapsed := time.Since(start).Seconds()
	st, out := evalPerf("time", elapsed, "s", a.Get("w"), a.Get("c"),
		fmt.Sprintf("%s (%.3fs)", banner, elapsed))
	return st, out
}

// checkSMTP: 220 greeting + EHLO, optional STARTTLS.
func checkSMTP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	port := a.Int(25, "p", "port")
	timeout := a.Duration(15*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return criticalf("connect to %s failed: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	r := bufio.NewReader(conn)
	greet, err := r.ReadString('\n')
	if err != nil || !strings.HasPrefix(greet, "220") {
		return criticalf("bad SMTP greeting from %s: %q", addr, firstLine(greet))
	}
	fmt.Fprintf(conn, "EHLO northplane.monitor\r\n")
	resp, err := readSMTPResponse(r)
	if err != nil || !strings.HasPrefix(resp, "250") {
		return criticalf("EHLO rejected by %s: %q", addr, firstLine(resp))
	}
	if a.Bool("starttls", "S") {
		fmt.Fprintf(conn, "STARTTLS\r\n")
		resp, _ := r.ReadString('\n')
		if !strings.HasPrefix(resp, "220") {
			return criticalf("STARTTLS rejected by %s: %q", addr, firstLine(resp))
		}
		tc := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: a.Bool("insecure")})
		if err := tc.HandshakeContext(ctx); err != nil {
			return criticalf("TLS handshake with %s failed: %v", addr, err)
		}
	}
	fmt.Fprintf(conn, "QUIT\r\n")
	elapsed := time.Since(start).Seconds()
	return evalPerf("time", elapsed, "s", a.Get("w"), a.Get("c"),
		fmt.Sprintf("SMTP %s responsive (%.3fs)", addr, elapsed))
}

func readSMTPResponse(r *bufio.Reader) (string, error) {
	var last string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return last, err
		}
		last = line
		if len(line) < 4 || line[3] != '-' {
			return line, nil
		}
	}
}

// checkIMAP: greeting "* OK", optional TLS (-S / port 993).
func checkIMAP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	useTLS := a.Bool("ssl", "S")
	port := a.Int(143, "p", "port")
	if useTLS && port == 143 {
		port = 993
	}
	timeout := a.Duration(15*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	start := time.Now()
	d := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{
			ServerName: host, InsecureSkipVerify: a.Bool("insecure")})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return criticalf("connect to %s failed: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	greet, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(greet, "* OK") {
		return criticalf("bad IMAP greeting from %s: %q", addr, firstLine(greet))
	}
	elapsed := time.Since(start).Seconds()
	return evalPerf("time", elapsed, "s", a.Get("w"), a.Get("c"),
		fmt.Sprintf("IMAP %s responsive (%.3fs)", addr, elapsed))
}

// checkNTP: SNTP offset query. Flags: -w/-c offset seconds.
func checkNTP(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	host := a.Host(t)
	timeout := a.Duration(10*time.Second, "t", "timeout")
	addr := net.JoinHostPort(host, "123")
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return criticalf("ntp dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// SNTP v4 client request
	req := make([]byte, 48)
	req[0] = 0x23 // LI=0 VN=4 Mode=3
	t1 := time.Now()
	putNTPTime(req[40:], t1)
	if _, err := conn.Write(req); err != nil {
		return criticalf("ntp send: %v", err)
	}
	resp := make([]byte, 48)
	if _, err := conn.Read(resp); err != nil {
		return criticalf("no NTP response from %s within %s", host, timeout)
	}
	t4 := time.Now()
	t2 := ntpTime(resp[32:]) // receive
	t3 := ntpTime(resp[40:]) // transmit
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	off := offset.Seconds()

	warn := a.Get("w", "warning")
	crit := a.Get("c", "critical")
	if warn == "" {
		warn = "0.5"
	}
	if crit == "" {
		crit = "2"
	}
	// offsets can be negative: evaluate magnitude
	mag := off
	if mag < 0 {
		mag = -mag
	}
	st, out := evalPerf("offset", mag, "s", warn, crit,
		fmt.Sprintf("clock offset %.4fs against %s", off, host))
	return st, out
}

const ntpEpochOffset = 2208988800 // 1900 → 1970

func putNTPTime(b []byte, t time.Time) {
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	b[0], b[1], b[2], b[3] = byte(secs>>24), byte(secs>>16), byte(secs>>8), byte(secs)
	b[4], b[5], b[6], b[7] = byte(frac>>24), byte(frac>>16), byte(frac>>8), byte(frac)
}

func ntpTime(b []byte) time.Time {
	secs := uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
	frac := uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	return time.Unix(int64(secs-ntpEpochOffset), int64(frac*1e9>>32))
}

// checkDNS: resolve and optionally compare. Flags: -H name to resolve,
// --server, --type A|AAAA|CNAME|MX|TXT, -a expected, -w/-c time s.
func checkDNS(ctx context.Context, t Target, a Args) (model.State, nagios.Output) {
	name := a.Host(t)
	qtype := strings.ToUpper(a.Get("type", "q"))
	if qtype == "" {
		qtype = "A"
	}
	resolver := net.DefaultResolver
	if server := a.Get("server", "s"); server != "" {
		if !strings.Contains(server, ":") {
			server += ":53"
		}
		resolver = &net.Resolver{PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, server)
			}}
	}
	start := time.Now()
	var values []string
	var err error
	switch qtype {
	case "A", "AAAA":
		var ips []net.IPAddr
		ips, err = resolver.LookupIPAddr(ctx, name)
		for _, ip := range ips {
			if qtype == "A" && ip.IP.To4() != nil {
				values = append(values, ip.IP.String())
			}
			if qtype == "AAAA" && ip.IP.To4() == nil {
				values = append(values, ip.IP.String())
			}
		}
	case "CNAME":
		var cname string
		cname, err = resolver.LookupCNAME(ctx, name)
		values = []string{strings.TrimSuffix(cname, ".")}
	case "MX":
		var mxs []*net.MX
		mxs, err = resolver.LookupMX(ctx, name)
		for _, mx := range mxs {
			values = append(values, strings.TrimSuffix(mx.Host, "."))
		}
	case "TXT":
		values, err = resolver.LookupTXT(ctx, name)
	default:
		return unknownf("dns: unsupported type %q", qtype)
	}
	elapsed := time.Since(start).Seconds()
	if err != nil {
		return criticalf("DNS %s lookup for %s failed: %v", qtype, name, err)
	}
	if len(values) == 0 {
		return criticalf("DNS %s lookup for %s returned no records", qtype, name)
	}
	if expect := a.Get("a", "expect"); expect != "" {
		found := false
		for _, v := range values {
			if v == expect {
				found = true
			}
		}
		if !found {
			return criticalf("DNS %s for %s = %s (expected %s)", qtype, name,
				strings.Join(values, ","), expect)
		}
	}
	sort.Strings(values)
	return evalPerf("time", elapsed, "s", a.Get("w"), a.Get("c"),
		fmt.Sprintf("%s %s → %s (%.3fs)", qtype, name, strings.Join(values, ","), elapsed))
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
