package smtpd

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/devrfc83/mailgun-passport/internal/metrics"
)

// tooManyConnections is sent instead of the greeting. RFC 5321 lets a server
// open with 421, and clients treat it as a temporary failure.
const tooManyConnections = "421 4.7.0 Too many connections, try again later\r\n"

// limitListener caps the number of open SMTP sessions. Every session may hold
// a whole message in memory, so the cap also bounds memory use. Connections
// over the cap are turned away at once rather than left waiting in the
// kernel's backlog, where the client would only see a slow greeting.
type limitListener struct {
	net.Listener
	slots   chan struct{}
	log     *slog.Logger
	metrics *metrics.Set

	closeOnce sync.Once
	closeErr  error
}

// Close closes the listener once and returns that result on every call:
// Server.Shutdown closes it before go-smtp's Shutdown closes it again.
func (l *limitListener) Close() error {
	l.closeOnce.Do(func() { l.closeErr = l.Listener.Close() })
	return l.closeErr
}

// newLimitListener caps l at n open connections; n of 0 or less means no cap.
// Either way, connections are counted in m.
func newLimitListener(l net.Listener, n int, log *slog.Logger, m *metrics.Set) net.Listener {
	if n <= 0 {
		return &limitListener{Listener: l, log: log, metrics: m}
	}
	return &limitListener{Listener: l, slots: make(chan struct{}, n), log: log, metrics: m}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.slots == nil {
			return l.accepted(c, func() {}), nil
		}
		select {
		case l.slots <- struct{}{}:
			return l.accepted(c, func() { <-l.slots }), nil
		default:
			l.metrics.Connections.Inc("refused")
			l.log.Warn("connection refused: SMTP_MAX_CONNECTIONS reached",
				"remote", c.RemoteAddr().String(), "max", cap(l.slots))
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(c, tooManyConnections)
			_ = c.Close()
		}
	}
}

func (l *limitListener) accepted(c net.Conn, free func()) net.Conn {
	l.metrics.Connections.Inc("accepted")
	l.metrics.ConnectionsOpen.Add(1)
	return &limitConn{Conn: c, release: sync.OnceFunc(func() {
		free()
		l.metrics.ConnectionsOpen.Add(-1)
	})}
}

type limitConn struct {
	net.Conn
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
