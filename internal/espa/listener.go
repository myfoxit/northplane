package espa

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/model"
)

const (
	// maxConnsPerListener caps concurrent connections per socket so a
	// misbehaving bridge cannot exhaust goroutines/file descriptors.
	maxConnsPerListener = 64
	// readTimeout is the rolling per-read deadline: any traffic resets
	// it, five silent minutes end the connection.
	readTimeout = 5 * time.Minute
	// writeTimeout bounds control/reply writes to a stuck peer.
	writeTimeout = 10 * time.Second
	// maxFrameBytes caps a single ESPA data block / ESPA-X document.
	maxFrameBytes = 64 * 1024
)

// emitFunc receives every protocol-level normalised event. The Manager
// wires it to rate limiting + publishing; tests wire it to a channel.
type emitFunc func(*model.NormEvent)

// listener owns one TCP socket and serves the pager protocol of its
// bound source on every accepted connection. The source is hot-swappable
// so a reconcile can re-point an address without touching the socket.
type listener struct {
	log      *slog.Logger
	addr     string // canonical configured address, e.g. "tcp://:2023"
	hostPort string // what net.Listen binds, e.g. ":2023"
	emit     func(boundSource, *model.NormEvent)

	ln   net.Listener
	done chan struct{} // closed when the accept loop returns

	mu     sync.Mutex
	bs     boundSource
	conns  map[net.Conn]struct{}
	closed bool
}

func newListener(log *slog.Logger, addr, hostPort string, emit func(boundSource, *model.NormEvent)) *listener {
	return &listener{
		log:      log,
		addr:     addr,
		hostPort: hostPort,
		emit:     emit,
		done:     make(chan struct{}),
		conns:    map[net.Conn]struct{}{},
	}
}

// setSource atomically replaces the bound source. New connections pick
// it up immediately; established sessions finish under the old one.
func (l *listener) setSource(bs boundSource) {
	l.mu.Lock()
	l.bs = bs
	l.mu.Unlock()
}

func (l *listener) source() boundSource {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bs
}

// start binds the socket and launches the accept loop. A bind error
// surfaces synchronously so reconcile can log and retry next tick.
func (l *listener) start() error {
	ln, err := net.Listen("tcp", l.hostPort)
	if err != nil {
		return err
	}
	l.ln = ln
	go l.acceptLoop()
	return nil
}

// stop closes the socket and every live connection, then waits for the
// accept loop to exit (bounded so shutdown can never hang).
func (l *listener) stop() {
	l.mu.Lock()
	l.closed = true
	conns := make([]net.Conn, 0, len(l.conns))
	for c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()

	if l.ln != nil {
		_ = l.ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	select {
	case <-l.done:
	case <-time.After(5 * time.Second): // never block shutdown indefinitely
	}
}

func (l *listener) acceptLoop() {
	defer close(l.done)
	for {
		c, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			l.log.Warn("espa: accept failed", "listen", l.addr, "err", err)
			time.Sleep(time.Second) // don't spin on persistent accept errors
			continue
		}
		if !l.track(c) {
			l.log.Warn("espa: connection cap reached; rejecting",
				"listen", l.addr, "remote", c.RemoteAddr().String(), "cap", maxConnsPerListener)
			_ = c.Close()
			continue
		}
		go l.serveConn(c)
	}
}

// track registers a connection, enforcing the per-listener cap.
func (l *listener) track(c net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || len(l.conns) >= maxConnsPerListener {
		return false
	}
	l.conns[c] = struct{}{}
	return true
}

func (l *listener) untrack(c net.Conn) {
	l.mu.Lock()
	delete(l.conns, c)
	l.mu.Unlock()
	_ = c.Close()
}

// serveConn runs one pager session. A panic in the protocol code must
// never take the listener down (SPEC robustness): recover and log.
func (l *listener) serveConn(c net.Conn) {
	defer l.untrack(c)
	defer func() {
		if r := recover(); r != nil {
			l.log.Error("espa: connection handler panic recovered",
				"listen", l.addr, "remote", c.RemoteAddr().String(), "panic", r)
		}
	}()

	bs := l.source()
	if bs.src == nil { // raced a shutdown/reconfigure; nothing to serve
		return
	}
	defSev := configSeverity(bs.src)
	emit := func(n *model.NormEvent) { l.emit(bs, n) }

	var err error
	switch bs.src.Type {
	case typeESPAX:
		err = serveESPAX(c, defSev, emit, l.log)
	default: // typeESPA
		err = serveESPA(c, defSev, emit, l.log)
	}
	if err != nil {
		l.log.Debug("espa: connection ended",
			"listen", l.addr, "remote", c.RemoteAddr().String(), "err", err)
	}
}

// deadlineReader refreshes the read deadline before every Read, giving
// the rolling inactivity timeout: any byte of traffic buys another
// readTimeout of patience.
type deadlineReader struct {
	c net.Conn
}

func (r deadlineReader) Read(p []byte) (int, error) {
	_ = r.c.SetReadDeadline(time.Now().Add(readTimeout))
	return r.c.Read(p)
}

// write sends raw bytes with a bounded deadline (control replies, XML).
func write(c net.Conn, b []byte) error {
	_ = c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := c.Write(b)
	return err
}

// sessionErr filters the errors a finished session reports: a clean
// remote close (EOF) is normal end-of-session, everything else bubbles
// up for debug logging.
func sessionErr(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
