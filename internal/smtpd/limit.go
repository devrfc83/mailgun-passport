package smtpd

import (
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
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
	slots chan struct{}
	log   *slog.Logger
}

func newLimitListener(l net.Listener, n int, log *slog.Logger) net.Listener {
	if n <= 0 {
		return l
	}
	return &limitListener{Listener: l, slots: make(chan struct{}, n), log: log}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitConn{Conn: c, release: sync.OnceFunc(func() { <-l.slots })}, nil
		default:
			l.log.Warn("connection refused: SMTP_MAX_CONNECTIONS reached",
				"remote", c.RemoteAddr().String(), "max", cap(l.slots))
			_ = c.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(c, tooManyConnections)
			_ = c.Close()
		}
	}
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
