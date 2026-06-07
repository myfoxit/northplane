package nagios

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
	"time"
)

// NRPE client (SPEC §8.4): v2 fixed-size packets and the v3/v4 variable
// format, plain TCP or TLS with certificate verification (deliberately
// unlike check_nrpe's anonymous-DH default — Go refuses ADH anyway, so
// TLS mode requires a daemon configured with real certificates).

const (
	nrpeQuery    = 1
	nrpeResponse = 2

	nrpeV2BufLen = 1024
	nrpeV2Size   = 2 + 2 + 4 + 2 + nrpeV2BufLen // 1034
)

// NRPEOptions configure a query.
type NRPEOptions struct {
	Address string        // host:port (default port 5666)
	Version int           // 2 or 4 (default 2 — maximum compatibility)
	TLS     *tls.Config   // nil = plaintext
	Timeout time.Duration // default 10s
}

// NRPEResult is the daemon's answer.
type NRPEResult struct {
	State  int
	Output string
}

// NRPEQuery runs a remote check: command plus arguments are joined with
// '!' per NRPE convention.
func NRPEQuery(ctx context.Context, o NRPEOptions, command string, args []string) (*NRPEResult, error) {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Version == 0 {
		o.Version = 2
	}
	addr := o.Address
	if !strings.Contains(addr, ":") {
		addr += ":5666"
	}
	d := net.Dialer{Timeout: o.Timeout}
	var conn net.Conn
	var err error
	if o.TLS != nil {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: o.TLS}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("nrpe: dial %s: %w", addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(o.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	query := command
	if len(args) > 0 {
		query += "!" + strings.Join(args, "!")
	}

	var pkt []byte
	switch o.Version {
	case 2:
		pkt, err = nrpeV2Packet(nrpeQuery, 0, query)
	case 3, 4:
		pkt, err = nrpeV4Packet(int16(o.Version), nrpeQuery, 0, query)
	default:
		return nil, fmt.Errorf("nrpe: unsupported version %d", o.Version)
	}
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(pkt); err != nil {
		return nil, fmt.Errorf("nrpe: send: %w", err)
	}
	return nrpeReadResponse(conn)
}

func nrpeV2Packet(ptype int16, result int16, payload string) ([]byte, error) {
	if len(payload) >= nrpeV2BufLen {
		return nil, fmt.Errorf("nrpe: command too long for v2 (%d bytes)", len(payload))
	}
	pkt := make([]byte, nrpeV2Size)
	binary.BigEndian.PutUint16(pkt[0:], 2)
	binary.BigEndian.PutUint16(pkt[2:], uint16(ptype))
	// crc at [4:8] stays zero for computation
	binary.BigEndian.PutUint16(pkt[8:], uint16(result))
	copy(pkt[10:], payload)
	crc := crc32.ChecksumIEEE(pkt)
	binary.BigEndian.PutUint32(pkt[4:], crc)
	return pkt, nil
}

func nrpeV4Packet(version, ptype int16, result int16, payload string) ([]byte, error) {
	bufLen := len(payload) + 1
	if bufLen < 1024 {
		bufLen = 1024 // daemons reject shorter v3/v4 buffers
	}
	pkt := make([]byte, 16+bufLen)
	binary.BigEndian.PutUint16(pkt[0:], uint16(version))
	binary.BigEndian.PutUint16(pkt[2:], uint16(ptype))
	binary.BigEndian.PutUint16(pkt[8:], uint16(result))
	// [10:12] alignment, [12:16] buffer length
	binary.BigEndian.PutUint32(pkt[12:], uint32(bufLen))
	copy(pkt[16:], payload)
	crc := crc32.ChecksumIEEE(pkt)
	binary.BigEndian.PutUint32(pkt[4:], crc)
	return pkt, nil
}

func nrpeReadResponse(conn net.Conn) (*NRPEResult, error) {
	head := make([]byte, 16)
	if _, err := readFull(conn, head[:10]); err != nil {
		return nil, fmt.Errorf("nrpe: read header: %w", err)
	}
	version := int16(binary.BigEndian.Uint16(head[0:]))
	ptype := int16(binary.BigEndian.Uint16(head[2:]))
	wireCRC := binary.BigEndian.Uint32(head[4:])
	result := int16(binary.BigEndian.Uint16(head[8:]))
	if ptype != nrpeResponse {
		return nil, fmt.Errorf("nrpe: unexpected packet type %d", ptype)
	}

	var payload []byte
	full := head[:10]
	switch version {
	case 2:
		buf := make([]byte, nrpeV2Size-10)
		if _, err := readFull(conn, buf); err != nil {
			return nil, fmt.Errorf("nrpe: read v2 body: %w", err)
		}
		full = append(full, buf...)
		payload = buf
	case 3, 4:
		if _, err := readFull(conn, head[10:16]); err != nil {
			return nil, fmt.Errorf("nrpe: read v3 header: %w", err)
		}
		bufLen := binary.BigEndian.Uint32(head[12:16])
		if bufLen > 1<<20 {
			return nil, fmt.Errorf("nrpe: oversized response (%d)", bufLen)
		}
		buf := make([]byte, bufLen)
		if _, err := readFull(conn, buf); err != nil {
			return nil, fmt.Errorf("nrpe: read v3 body: %w", err)
		}
		full = append(full, head[10:16]...)
		full = append(full, buf...)
		payload = buf
	default:
		return nil, fmt.Errorf("nrpe: unsupported response version %d", version)
	}

	// CRC verification with the crc field zeroed.
	check := make([]byte, len(full))
	copy(check, full)
	binary.BigEndian.PutUint32(check[4:], 0)
	if crc32.ChecksumIEEE(check) != wireCRC {
		return nil, fmt.Errorf("nrpe: crc mismatch")
	}

	out := payload
	if i := indexByte(out, 0); i >= 0 {
		out = out[:i]
	}
	return &NRPEResult{State: int(result), Output: string(out)}, nil
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := conn.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
