package mailin

// Minimal IMAP4rev1 client (RFC 3501) — stdlib only.
//
// Scope is deliberately tiny: what the e-mail ingress poller (SPEC §7.5,
// "E-Mail | IMAP-Poller") needs and nothing more — connect (implicit TLS
// or plain), LOGIN, SELECT, SEARCH UNSEEN, FETCH <id> (RFC822), STORE
// <id> +FLAGS (\Seen), LOGOUT. It is tolerant of servers interleaving
// extra untagged responses and handles IMAP literals ({n}CRLF) in FETCH
// data correctly. There is no pipelining: one tagged command at a time.

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// ioTimeout bounds every socket operation so a wedged or malicious server
// cannot pin a poller goroutine forever (no goroutine leaks).
const ioTimeout = 30 * time.Second

// imapClient is a single, non-concurrent IMAP connection.
type imapClient struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
	tag  int
}

// dialIMAP opens a connection. tls=true uses implicit TLS (port 993);
// tls=false is a plain TCP connection (used by tests). The server greeting
// is consumed before returning.
func dialIMAP(addr string, useTLS bool, serverName string) (*imapClient, error) {
	d := &net.Dialer{Timeout: ioTimeout}
	var conn net.Conn
	var err error
	if useTLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, &tls.Config{ServerName: serverName})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	c := &imapClient{
		conn: conn,
		r:    bufio.NewReader(conn),
		w:    bufio.NewWriter(conn),
	}
	// Greeting: a single untagged line, e.g. "* OK [CAPABILITY …] ready".
	c.extend()
	line, err := c.readLine()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("imap: read greeting: %w", err)
	}
	if !strings.HasPrefix(line, "* OK") && !strings.HasPrefix(line, "* PREAUTH") {
		_ = conn.Close()
		return nil, fmt.Errorf("imap: unexpected greeting: %q", line)
	}
	return c, nil
}

func (c *imapClient) Close() error { return c.conn.Close() }

// extend pushes the read/write deadline forward; called before each I/O so
// a long-but-progressing session (many fetches) is not killed mid-stream.
func (c *imapClient) extend() {
	_ = c.conn.SetDeadline(time.Now().Add(ioTimeout))
}

// nextTag returns monotonically increasing command tags (a001, a002, …).
func (c *imapClient) nextTag() string {
	c.tag++
	return fmt.Sprintf("a%03d", c.tag)
}

// readLine reads one CRLF-terminated protocol line (CRLF stripped).
func (c *imapClient) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// writeCmd sends "<tag> <cmd>\r\n".
func (c *imapClient) writeCmd(tag, cmd string) error {
	c.extend()
	if _, err := c.w.WriteString(tag + " " + cmd + "\r\n"); err != nil {
		return err
	}
	return c.w.Flush()
}

// command runs a tagged command, feeding every untagged line to untagged
// (which may itself read literals via the client). It returns once the
// tagged completion line ("<tag> OK|NO|BAD …") is seen.
func (c *imapClient) command(cmd string, untagged func(line string) error) error {
	tag := c.nextTag()
	if err := c.writeCmd(tag, cmd); err != nil {
		return err
	}
	for {
		c.extend()
		line, err := c.readLine()
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, tag+" "):
			resp := strings.TrimSpace(line[len(tag)+1:])
			if strings.HasPrefix(resp, "OK") {
				return nil
			}
			return fmt.Errorf("imap: %s failed: %s", firstWord(cmd), resp)
		case strings.HasPrefix(line, "* "):
			if untagged != nil {
				if err := untagged(line); err != nil {
					return err
				}
			}
		default:
			// Continuation requests ("+ ", unexpected since our commands send
			// no literals) and stray lines (blank/bare lines some servers
			// emit) are both ignored tolerantly.
		}
	}
}

// Login authenticates with LOGIN, properly quoting/escaping the arguments
// so credentials containing spaces, quotes or backslashes are safe.
func (c *imapClient) Login(user, pass string) error {
	return c.command("LOGIN "+quoteIMAP(user)+" "+quoteIMAP(pass), nil)
}

// Select opens a mailbox (default folder handled by caller).
func (c *imapClient) Select(folder string) error {
	return c.command("SELECT "+quoteIMAP(folder), nil)
}

// SearchUnseen returns message sequence numbers of UNSEEN messages.
func (c *imapClient) SearchUnseen() ([]string, error) {
	var ids []string
	err := c.command("SEARCH UNSEEN", func(line string) error {
		// "* SEARCH 1 2 3" (or "* SEARCH" when empty). Be tolerant of
		// other untagged lines the server may interleave.
		rest := strings.TrimPrefix(line, "* ")
		if u := strings.ToUpper(rest); strings.HasPrefix(u, "SEARCH") {
			fields := strings.Fields(rest[len("SEARCH"):])
			ids = append(ids, fields...)
		}
		return nil
	})
	return ids, err
}

// Fetch retrieves the full RFC822 message for one sequence id. It parses
// the IMAP literal ({n}CRLF) that carries the body and returns the raw
// bytes (headers + body) for net/mail.
func (c *imapClient) Fetch(id string) ([]byte, error) {
	var body []byte
	err := c.command("FETCH "+id+" (RFC822)", func(line string) error {
		// The response is "* <id> FETCH (... RFC822 {n}\r\n<n bytes>...)".
		// A literal is announced by a trailing "{n}" on the line; read
		// exactly n bytes, then continue consuming the remainder of the
		// FETCH response (closing parenthesis etc.) as ordinary lines.
		for {
			n, ok := literalSize(line)
			if !ok {
				return nil
			}
			buf := make([]byte, n)
			c.extend()
			if _, err := io.ReadFull(c.r, buf); err != nil {
				return fmt.Errorf("imap: read literal: %w", err)
			}
			body = buf
			// Continue reading the tail of this untagged response; it may
			// contain further literals (it won't for RFC822, but stay safe).
			c.extend()
			next, err := c.readLine()
			if err != nil {
				return err
			}
			line = next
		}
	})
	if err != nil {
		return nil, err
	}
	if body == nil {
		return nil, fmt.Errorf("imap: no RFC822 literal in FETCH %s", id)
	}
	return body, nil
}

// MarkSeen sets the \Seen flag on a message (STORE +FLAGS).
func (c *imapClient) MarkSeen(id string) error {
	return c.command("STORE "+id+` +FLAGS (\Seen)`, nil)
}

// Logout ends the session cleanly.
func (c *imapClient) Logout() error {
	return c.command("LOGOUT", nil)
}

// quoteIMAP renders an IMAP quoted string per RFC 3501 §4.3: wrap in double
// quotes, backslash-escaping '"' and '\'.
func quoteIMAP(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
	return b.String()
}

// literalSize reports the byte count of a trailing IMAP literal marker
// "{n}" at the end of line, if present.
func literalSize(line string) (int, bool) {
	i := strings.LastIndexByte(line, '{')
	if i < 0 || !strings.HasSuffix(line, "}") {
		return 0, false
	}
	num := line[i+1 : len(line)-1]
	// RFC allows a non-synchronizing "{n+}"; tolerate the trailing '+'.
	num = strings.TrimSuffix(num, "+")
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}
